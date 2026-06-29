package payroll

// calc.go
// =============================================================================
// The (simplified) UK PAYE + National Insurance calculation engine.
//
// PURE: every function here works on int64 PENCE and a RateTable value — no DB, no
// pgtype, no float (shopspring/decimal does the rounding, HALF-UP, like the money
// package). That keeps it unit-testable in isolation (calc_test.go pins it to known
// HMRC figures) and the engine independent of where the rates came from.
//
// What it models (v1):
//   - Cumulative PAYE for L-suffix codes, plus the flat codes BR / D0 / D1 / NT, and
//     the Week 1 / Month 1 (non-cumulative) basis. Region is taken from the tax-code
//     PREFIX: S → Scotland, C → Wales, otherwise rest-of-UK.
//   - Employee + employer NI for the seeded category letters, in two modes: ordinary
//     employees on the MONTHLY thresholds, company directors on the ANNUAL thresholds
//     assessed CUMULATIVELY across the year.
//
// What it does NOT model yet (see BACKLOG.md): statutory-pay calculation (amounts are
// taken as entered), pension contributions, student-loan deductions, K-codes
// (negative free pay), and the directors' "alternative arrangements" final-month
// recalculation (treated as ordinary monthly here).
// =============================================================================

import (
	"strconv"
	"strings"

	"github.com/shopspring/decimal"
)

// PayslipInputs are the period pay components + deductions the engine rolls up.
type PayslipInputs struct {
	BasicPay             int64
	Overtime             int64
	Bonus                int64
	Commission           int64
	Allowance            int64
	AbsencePayments      int64
	HolidayPay           int64
	OtherPayments        int64
	PayNotSubjectToTaxNI int64

	StatutorySick                int64
	StatutoryMaternity           int64
	StatutoryPaternity           int64
	StatutoryAdoption            int64
	SharedParental               int64
	StatutoryNeonatal            int64
	StatutoryParentalBereavement int64

	PayrollGiving         int64
	OtherDeductionsNetPay int64
	ItemsClass1NicNotPaye int64
	SalarySacrifice       int64
}

// YTDPrior is the employee's cumulative figures BEFORE this period — the input the
// cumulative tax code and the director NI need. Zero for period 1.
type YTDPrior struct {
	TaxablePay  int64
	TaxDeducted int64
	NiablePay   int64
}

// PayslipResult is the engine's computed figures for the period.
type PayslipResult struct {
	GrossPay        int64
	TaxablePay      int64
	NiablePay       int64
	TaxDeducted     int64
	EmployeeNI      int64
	EmployerNI      int64
	EmployeePension int64
	StudentLoan     int64
	NetPay          int64
}

// ComputePayslip rolls up the inputs and runs the PAYE + NI calculation for one
// employee's period. ok is false only when the NI category letter has no seeded rate
// (the caller maps that to a validation error). nicCalculation is one of
// "employee" / "director" / "director_alternative".
func ComputePayslip(
	rt RateTable,
	taxCode string,
	niCategory string,
	nicCalculation string,
	week1Month1 bool,
	period int,
	in PayslipInputs,
	prior YTDPrior,
) (PayslipResult, bool) {
	catRate, ok := rt.CategoryRate(niCategory)
	if !ok {
		return PayslipResult{}, false
	}

	// --- Roll-up ---------------------------------------------------------------
	statutory := in.StatutorySick + in.StatutoryMaternity + in.StatutoryPaternity +
		in.StatutoryAdoption + in.SharedParental + in.StatutoryNeonatal + in.StatutoryParentalBereavement

	gross := in.BasicPay + in.Overtime + in.Bonus + in.Commission + in.Allowance +
		in.AbsencePayments + in.HolidayPay + in.OtherPayments + in.PayNotSubjectToTaxNI + statutory

	// Taxable pay excludes the explicitly tax/NI-free pay and salary sacrifice;
	// statutory pay IS taxable. Niable pay additionally counts Class-1-NIC items.
	taxable := gross - in.PayNotSubjectToTaxNI - in.SalarySacrifice
	if taxable < 0 {
		taxable = 0
	}
	niable := taxable + in.ItemsClass1NicNotPaye

	// --- PAYE ------------------------------------------------------------------
	tax := computePAYE(rt, taxCode, prior.TaxablePay, prior.TaxDeducted, taxable, period, week1Month1)

	// HMRC "regulatory limit": the PAYE deducted in a period may not exceed 50% of
	// that period's taxable pay (so a large cumulative catch-up can't swallow a whole
	// payslip). Only caps a positive deduction — an in-year refund (negative tax) is
	// left alone. The under-deducted remainder carries forward automatically: next
	// period's cumulative `taxToDate − priorTaxPaid` reads the actually-stored (capped)
	// tax, so the shortfall reappears and is re-tested against the cap. NI is exempt
	// from this limit and is unaffected below.
	if maxTax := pctOf(taxable, 5000); tax > maxTax {
		tax = maxTax
	}

	// --- NI --------------------------------------------------------------------
	director := nicCalculation == "director"
	var employeeNI, employerNI int64
	if director {
		employeeNI = directorEmployeeNI(rt.NI, catRate, prior.NiablePay, niable)
		employerNI = directorEmployerNI(rt.NI, catRate, prior.NiablePay, niable)
	} else {
		employeeNI = employeeNIMonthly(rt.NI, catRate, niable)
		employerNI = employerNIMonthly(rt.NI, catRate, niable)
	}

	// --- Net pay (v1: pension / student loan deferred = 0) ---------------------
	net := gross - tax - employeeNI - in.PayrollGiving - in.OtherDeductionsNetPay - in.SalarySacrifice

	return PayslipResult{
		GrossPay:    gross,
		TaxablePay:  taxable,
		NiablePay:   niable,
		TaxDeducted: tax,
		EmployeeNI:  employeeNI,
		EmployerNI:  employerNI,
		NetPay:      net,
	}, true
}

// =============================================================================
// PAYE
// =============================================================================

// computePAYE returns the PAYE tax for the period. It parses the tax code's region
// prefix and free-pay number, handles the flat codes (BR/D0/D1/NT), and otherwise
// runs the cumulative (or Week 1/Month 1) free-pay + banded-tax calculation.
func computePAYE(rt RateTable, taxCode string, priorTaxable, priorTaxPaid, periodTaxable int64, period int, w1m1 bool) int64 {
	code := strings.ToUpper(strings.TrimSpace(taxCode))
	if code == "" {
		code = strings.ToUpper(strings.TrimSpace(rt.DefaultTaxCode))
	}

	// Region prefix: S (Scotland) or C (Wales). What's left is the bare code.
	region := "rUK"
	if strings.HasPrefix(code, "S") && code != "S" {
		region, code = "S", code[1:]
	} else if strings.HasPrefix(code, "C") && code != "C" {
		region, code = "C", code[1:]
	}
	bands := rt.PayeBands[region]
	if len(bands) == 0 {
		bands = rt.PayeBands["rUK"]
	}

	// Flat codes — no personal allowance, a single rate on the period's taxable pay.
	switch code {
	case "NT":
		return 0
	case "BR":
		return pctOf(periodTaxable, bandRate(bands, 0))
	case "D0":
		return pctOf(periodTaxable, bandRate(bands, 1))
	case "D1":
		return pctOf(periodTaxable, bandRate(bands, 2))
	}

	// Numeric (L/M/N/T suffix) code → free pay = number × 10 (in pounds).
	freePayAnnual := freePayAnnualMinor(code)

	if w1m1 {
		// Non-cumulative: one period's free pay + one period's bands, on this period only.
		freePeriod := freePayAnnual / int64(rt.periodCount())
		taxableAfter := periodTaxable - freePeriod
		if taxableAfter < 0 {
			taxableAfter = 0
		}
		return bandedTax(taxableAfter, bands, 1, rt.periodCount())
	}

	// Cumulative: free pay and bands scale by period/periodCount across the year;
	// the period's tax is (tax due to date) − (tax already paid to date).
	cumTaxable := priorTaxable + periodTaxable
	freeToDate := apportion(freePayAnnual, int64(period), int64(rt.periodCount()))
	taxableToDate := cumTaxable - freeToDate
	if taxableToDate < 0 {
		taxableToDate = 0
	}
	taxToDate := bandedTax(taxableToDate, bands, period, rt.periodCount())
	return taxToDate - priorTaxPaid
}

// bandedTax applies the income-tax bands to a (cumulative) taxable amount. The band
// thresholds are annual, so they're apportioned by period/periodCount to the point in
// the year. The final figure is rounded HALF-UP to whole pence.
func bandedTax(taxable int64, bands []Band, period, periodCount int) int64 {
	if taxable <= 0 || len(bands) == 0 {
		return 0
	}
	remaining := taxable
	var prevTop int64
	total := decimal.Zero
	for _, b := range bands {
		var width int64
		if b.UpperMinor == 0 { // open-ended top band
			width = remaining
		} else {
			top := apportion(b.UpperMinor, int64(period), int64(periodCount))
			width = top - prevTop
			prevTop = top
		}
		if width < 0 {
			width = 0
		}
		amt := min64(remaining, width)
		if amt > 0 {
			total = total.Add(pctDecimal(amt, b.RateBps))
			remaining -= amt
		}
		if remaining <= 0 {
			break
		}
	}
	return total.Round(0).IntPart()
}

// freePayAnnualMinor turns the numeric part of a tax code into the annual free pay in
// pence: "1257L" → 1257 × 10 = £12,570 = 1257000. A code with no digits → 0.
func freePayAnnualMinor(code string) int64 {
	var digits strings.Builder
	for _, r := range code {
		if r >= '0' && r <= '9' {
			digits.WriteRune(r)
		} else {
			break // the suffix letter(s) end the number
		}
	}
	n, err := strconv.ParseInt(digits.String(), 10, 64)
	if err != nil {
		return 0
	}
	return n * 10 * 100 // ×10 (pounds) ×100 (pence)
}

// bandRate returns the rate (bps) of the band at idx, clamped to the last band — so
// D1 on a region with fewer bands still resolves to its top rate.
func bandRate(bands []Band, idx int) int32 {
	if len(bands) == 0 {
		return 0
	}
	if idx >= len(bands) {
		idx = len(bands) - 1
	}
	return bands[idx].RateBps
}

// =============================================================================
// NATIONAL INSURANCE
// =============================================================================

// employeeNIMonthly: employee NI on a single period's niable pay (ordinary employee).
func employeeNIMonthly(ni NIThresholds, r NICategoryRate, niable int64) int64 {
	return niBetween(niable, ni.PTMonthlyMinor, ni.UELMonthlyMinor, r.EmployeeMainBps, r.EmployeeUpperBps)
}

// employerNIMonthly: employer NI on a single period's niable pay.
func employerNIMonthly(ni NIThresholds, r NICategoryRate, niable int64) int64 {
	return pctOf(above(niable, ni.STMonthlyMinor), r.EmployerBps)
}

// directorEmployeeNI: directors are assessed CUMULATIVELY on the ANNUAL thresholds —
// the period's contribution is (liability on cumulative pay) − (liability on prior pay).
func directorEmployeeNI(ni NIThresholds, r NICategoryRate, priorNiable, periodNiable int64) int64 {
	cum := priorNiable + periodNiable
	liabTo := niBetween(cum, ni.PTAnnualMinor, ni.UELAnnualMinor, r.EmployeeMainBps, r.EmployeeUpperBps)
	liabPrior := niBetween(priorNiable, ni.PTAnnualMinor, ni.UELAnnualMinor, r.EmployeeMainBps, r.EmployeeUpperBps)
	return liabTo - liabPrior
}

// directorEmployerNI: cumulative employer NI on the annual secondary threshold.
func directorEmployerNI(ni NIThresholds, r NICategoryRate, priorNiable, periodNiable int64) int64 {
	cum := priorNiable + periodNiable
	liabTo := pctOf(above(cum, ni.STAnnualMinor), r.EmployerBps)
	liabPrior := pctOf(above(priorNiable, ni.STAnnualMinor), r.EmployerBps)
	return liabTo - liabPrior
}

// niBetween computes NI across two rate bands: mainBps on earnings between lower and
// upper, upperBps on earnings above upper. Each band is rounded HALF-UP.
func niBetween(amount, lower, upper int64, mainBps, upperBps int32) int64 {
	if amount <= lower {
		return 0
	}
	main := min64(amount, upper) - lower
	if main < 0 {
		main = 0
	}
	return pctOf(main, mainBps) + pctOf(above(amount, upper), upperBps)
}

// =============================================================================
// SMALL HELPERS
// =============================================================================

func (rt RateTable) periodCount() int {
	if rt.PeriodCount <= 0 {
		return 12
	}
	return rt.PeriodCount
}

// above returns max(0, amount − threshold).
func above(amount, threshold int64) int64 {
	if amount <= threshold {
		return 0
	}
	return amount - threshold
}

func min64(a, b int64) int64 {
	if a < b {
		return a
	}
	return b
}

// apportion returns round_half_up(value × num / den) in pence.
func apportion(value, num, den int64) int64 {
	if den == 0 {
		return 0
	}
	return decimal.NewFromInt(value).Mul(decimal.NewFromInt(num)).Div(decimal.NewFromInt(den)).Round(0).IntPart()
}

// pctOf returns round_half_up(amount × bps / 10000) in pence.
func pctOf(amount int64, bps int32) int64 {
	return pctDecimal(amount, bps).Round(0).IntPart()
}

// pctDecimal is the un-rounded amount × bps / 10000 (so callers can sum then round).
func pctDecimal(amount int64, bps int32) decimal.Decimal {
	return decimal.NewFromInt(amount).Mul(decimal.NewFromInt(int64(bps))).Div(decimal.NewFromInt(10000))
}
