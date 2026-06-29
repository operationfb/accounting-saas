package payroll

// calc_test.go
// =============================================================================
// The financial-correctness gate for the PAYE/NI engine. Pure (no DB): it builds a
// RateTable literal with the 2026/27 figures (mirroring db/seeds/payroll_rates_2026_27.sql)
// and pins the engine to known HMRC results — including the exact figures from the
// reference payslips:
//   - £700/mo employee, cat A → £42.45 employer NI, £0 employee NI, £0 tax
//   - £1,047/mo director, cat A → £0 employer NI (cumulative pay under the £5,000 ST)
//   - 1257L under the monthly personal allowance → £0 tax
// =============================================================================

import "testing"

// testRates2026 mirrors the seed so the engine tests don't need a database.
func testRates2026() RateTable {
	return RateTable{
		TaxYearStart:                2026,
		DefaultTaxCode:              "1257L",
		PeriodCount:                 12,
		EmploymentAllowanceCapMinor: 1050000,
		PayeBands: map[string][]Band{
			"rUK": {
				{UpperMinor: 3770000, RateBps: 2000},
				{UpperMinor: 12514000, RateBps: 4000},
				{UpperMinor: 0, RateBps: 4500},
			},
			"C": {
				{UpperMinor: 3770000, RateBps: 2000},
				{UpperMinor: 12514000, RateBps: 4000},
				{UpperMinor: 0, RateBps: 4500},
			},
			"S": {
				{UpperMinor: 396700, RateBps: 1900},
				{UpperMinor: 1695600, RateBps: 2000},
				{UpperMinor: 3109200, RateBps: 2100},
				{UpperMinor: 6243000, RateBps: 4200},
				{UpperMinor: 12514000, RateBps: 4500},
				{UpperMinor: 0, RateBps: 4800},
			},
		},
		NI: NIThresholds{
			LELAnnualMinor: 670800, LELMonthlyMinor: 55900,
			PTAnnualMinor: 1257000, PTMonthlyMinor: 104800,
			STAnnualMinor: 500000, STMonthlyMinor: 41700,
			UELAnnualMinor: 5027000, UELMonthlyMinor: 418900,
		},
		NICategories: map[string]NICategoryRate{
			"A": {EmployeeMainBps: 800, EmployeeUpperBps: 200, EmployerBps: 1500},
			"C": {EmployeeMainBps: 0, EmployeeUpperBps: 0, EmployerBps: 1500},
		},
	}
}

// TestEmployeeNIReferenceFigure: £700/mo, cat A, ordinary employee → employer NI
// £42.45 = 15% × (£700 − £417 monthly ST); employee NI £0 (under the PT).
func TestEmployeeNIReferenceFigure(t *testing.T) {
	rt := testRates2026()
	res, ok := ComputePayslip(rt, "1257L", "A", "employee", false, 3,
		PayslipInputs{BasicPay: 70000}, YTDPrior{})
	if !ok {
		t.Fatal("category A should be seeded")
	}
	if res.EmployerNI != 4245 {
		t.Errorf("employer NI = %d, want 4245 (£42.45)", res.EmployerNI)
	}
	if res.EmployeeNI != 0 {
		t.Errorf("employee NI = %d, want 0 (under PT)", res.EmployeeNI)
	}
	if res.TaxDeducted != 0 {
		t.Errorf("tax = %d, want 0 (under monthly allowance)", res.TaxDeducted)
	}
	if res.NetPay != 70000 {
		t.Errorf("net pay = %d, want 70000 (£700)", res.NetPay)
	}
}

// TestDirectorNICumulativeZero: £1,047/mo director, cat A, month 3 with two prior
// months of niable pay — cumulative £3,141 is under the £5,000 annual ST and the
// £12,570 annual PT, so BOTH employer and employee NI are £0 (matches the reference).
func TestDirectorNICumulativeZero(t *testing.T) {
	rt := testRates2026()
	prior := YTDPrior{TaxablePay: 209400, NiablePay: 209400} // 2 × £1,047
	res, ok := ComputePayslip(rt, "1257L", "A", "director", false, 3,
		PayslipInputs{BasicPay: 104700}, prior)
	if !ok {
		t.Fatal("category A should be seeded")
	}
	if res.EmployerNI != 0 {
		t.Errorf("director employer NI = %d, want 0 (cumulative under ST)", res.EmployerNI)
	}
	if res.EmployeeNI != 0 {
		t.Errorf("director employee NI = %d, want 0 (cumulative under PT)", res.EmployeeNI)
	}
	if res.TaxDeducted != 0 {
		t.Errorf("tax = %d, want 0", res.TaxDeducted)
	}
}

// TestDirectorEmployerNICrossesAnnualThreshold: once a director's cumulative pay
// passes the £5,000 annual ST, the period that crosses it bears employer NI on the
// excess only. At cumulative £6,000 (prior £4,500 + £1,500) the excess over £5,000 is
// £1,000 → 15% = £150.
func TestDirectorEmployerNICrossesAnnualThreshold(t *testing.T) {
	rt := testRates2026()
	res, _ := ComputePayslip(rt, "1257L", "A", "director", false, 5,
		PayslipInputs{BasicPay: 150000}, YTDPrior{NiablePay: 450000, TaxablePay: 450000})
	if res.EmployerNI != 15000 {
		t.Errorf("director employer NI = %d, want 15000 (£150 on the £1,000 over ST)", res.EmployerNI)
	}
}

// TestEmployeeNIAboveThreshold: £2,000/mo, cat A → employee NI 8% on (£2,000 − £1,048
// PT) = £952 → £76.16; employer NI 15% on (£2,000 − £417 ST) = £1,583 → £237.45.
func TestEmployeeNIAboveThreshold(t *testing.T) {
	rt := testRates2026()
	res, _ := ComputePayslip(rt, "1257L", "A", "employee", false, 1,
		PayslipInputs{BasicPay: 200000}, YTDPrior{})
	if res.EmployeeNI != 7616 {
		t.Errorf("employee NI = %d, want 7616 (£76.16)", res.EmployeeNI)
	}
	if res.EmployerNI != 23745 {
		t.Errorf("employer NI = %d, want 23745 (£237.45)", res.EmployerNI)
	}
}

// TestFlatTaxCodes: BR taxes everything at basic (20%), D0 at higher (40%), NT at 0,
// with no personal allowance.
func TestFlatTaxCodes(t *testing.T) {
	rt := testRates2026()
	cases := []struct {
		code string
		want int64
	}{
		{"BR", 20000}, // 20% of £1,000
		{"D0", 40000}, // 40% of £1,000
		{"D1", 45000}, // 45% of £1,000
		{"NT", 0},
	}
	for _, c := range cases {
		res, _ := ComputePayslip(rt, c.code, "A", "employee", false, 1,
			PayslipInputs{BasicPay: 100000}, YTDPrior{})
		if res.TaxDeducted != c.want {
			t.Errorf("%s: tax = %d, want %d", c.code, res.TaxDeducted, c.want)
		}
	}
}

// TestCumulativeTaxBasicRate: £2,000/mo on 1257L, month 1 → all within the month's
// basic-rate band. Taxable = £2,000 − £1,047.50 free pay = £952.50; the monthly basic
// band is £3,141.67, so it's all at 20% = £190.50.
func TestCumulativeTaxBasicRate(t *testing.T) {
	rt := testRates2026()
	res, _ := ComputePayslip(rt, "1257L", "A", "employee", false, 1,
		PayslipInputs{BasicPay: 200000}, YTDPrior{})
	if res.TaxDeducted != 19050 {
		t.Errorf("tax = %d, want 19050 (£190.50)", res.TaxDeducted)
	}
}

// TestCumulativeTaxIntoHigherRate: £5,000/mo on 1257L, month 1. The monthly basic band
// is only £3,141.67, so taxable of £3,952.50 spills into 40%:
// £3,141.67 × 20% + £810.83 × 40% = £628.33 + £324.33 = £952.67.
func TestCumulativeTaxIntoHigherRate(t *testing.T) {
	rt := testRates2026()
	res, _ := ComputePayslip(rt, "1257L", "A", "employee", false, 1,
		PayslipInputs{BasicPay: 500000}, YTDPrior{})
	if res.TaxDeducted != 95267 {
		t.Errorf("tax = %d, want 95267 (£952.67)", res.TaxDeducted)
	}
}

// TestPAYERegulatoryLimit: the period's PAYE can't exceed 50% of the period's
// taxable pay, even when a cumulative catch-up (large prior taxable with no tax yet
// deducted) would otherwise deduct far more. NI is unaffected (paid in full).
func TestPAYERegulatoryLimit(t *testing.T) {
	rt := testRates2026()
	// Month 2, £10,000 of prior taxable pay on which NO tax was deducted, then a
	// modest £1,000 this period. The cumulative tax due is several thousand pounds,
	// but the period deduction is capped at 50% of £1,000 = £500.
	res, _ := ComputePayslip(rt, "1257L", "A", "employee", false, 2,
		PayslipInputs{BasicPay: 100000},
		YTDPrior{TaxablePay: 1000000, TaxDeducted: 0, NiablePay: 1000000})
	if res.TaxDeducted != 50000 {
		t.Errorf("tax = %d, want 50000 (£500 = 50%% of £1,000 taxable)", res.TaxDeducted)
	}
	// Employee NI is the normal monthly figure, untouched by the tax cap:
	// 8% of (£1,000 − £1,048 PT) is 0 here — but assert it's the real monthly result.
	wantNI := employeeNIMonthly(rt.NI, rt.NICategories["A"], 100000)
	if res.EmployeeNI != wantNI {
		t.Errorf("employee NI = %d, want %d (NI is exempt from the regulatory limit)", res.EmployeeNI, wantNI)
	}
}

// TestScottishCodeUsesScottishBands: an S-prefixed code must tax differently from the
// rest-of-UK code at the same (high) income, because Scotland has extra bands/rates.
func TestScottishCodeUsesScottishBands(t *testing.T) {
	rt := testRates2026()
	in := PayslipInputs{BasicPay: 600000} // £6,000/mo, well into the diverging bands
	rUK, _ := ComputePayslip(rt, "1257L", "A", "employee", false, 1, in, YTDPrior{})
	scot, _ := ComputePayslip(rt, "S1257L", "A", "employee", false, 1, in, YTDPrior{})
	if rUK.TaxDeducted == scot.TaxDeducted {
		t.Errorf("Scottish tax (%d) should differ from rUK tax (%d)", scot.TaxDeducted, rUK.TaxDeducted)
	}
}

// TestUnknownCategoryRejected: a category letter with no seeded rate returns ok=false
// so the service can surface a clear error rather than mis-calculate.
func TestUnknownCategoryRejected(t *testing.T) {
	rt := testRates2026()
	if _, ok := ComputePayslip(rt, "1257L", "X", "employee", false, 1, PayslipInputs{BasicPay: 100000}, YTDPrior{}); ok {
		t.Error("expected ok=false for unseeded NI category X")
	}
}
