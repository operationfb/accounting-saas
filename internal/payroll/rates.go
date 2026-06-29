package payroll

// rates.go
// =============================================================================
// The statutory HMRC rate data, loaded from the GLOBAL reference tables (db/payroll
// payroll_tax_years / payroll_paye_bands / payroll_ni_thresholds /
// payroll_ni_category_rates) into a plain in-memory RateTable value.
//
// The point of this split: the calculation engine (calc.go) is PURE — it takes a
// RateTable as a parameter and never touches the database — so it stays trivially
// unit-testable (the tests build a RateTable literal) and the statutory figures live
// as data (seed SQL) rather than hardcoded Go constants. This file is the only place
// that knows how to turn DB rows into that value.
// =============================================================================

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"

	payrolldb "github.com/operationfb/accounting-saas/db/payroll"
	"github.com/operationfb/accounting-saas/internal/kernel"
)

// Band is one income-tax band ABOVE the personal allowance. UpperMinor is the
// band's cumulative upper bound of taxable income (in pence); 0 marks the
// open-ended TOP band. Bands within a region are ordered lowest-rate first.
type Band struct {
	UpperMinor int64
	RateBps    int32
}

// NIThresholds holds the National Insurance thresholds, both the annual figures
// (used for company directors, who are assessed cumulatively across the year) and
// the published rounded MONTHLY figures (used for ordinary employees).
type NIThresholds struct {
	LELAnnualMinor, LELMonthlyMinor int64 // Lower Earnings Limit
	PTAnnualMinor, PTMonthlyMinor   int64 // Primary Threshold (employee)
	STAnnualMinor, STMonthlyMinor   int64 // Secondary Threshold (employer)
	UELAnnualMinor, UELMonthlyMinor int64 // Upper Earnings Limit
}

// NICategoryRate is the contribution rates for one NI category letter (basis points).
type NICategoryRate struct {
	EmployeeMainBps  int32 // PT → UEL
	EmployeeUpperBps int32 // above UEL
	EmployerBps      int32 // above ST
}

// RateTable is everything the engine needs for one tax year: the per-region income
// tax bands, the NI thresholds + per-category rates, and the year-level config.
type RateTable struct {
	TaxYearStart                int
	DefaultTaxCode              string
	PeriodCount                 int
	EmploymentAllowanceCapMinor int64
	PayeBands                   map[string][]Band // region ("rUK"/"S"/"C") → bands
	NI                          NIThresholds
	NICategories                map[string]NICategoryRate // letter → rates
}

// CategoryRate returns the NI rates for a category letter; ok is false when that
// letter has no seeded row (the service turns this into a clear validation error
// rather than silently mis-calculating).
func (rt RateTable) CategoryRate(letter string) (NICategoryRate, bool) {
	r, ok := rt.NICategories[letter]
	return r, ok
}

// LoadRateTable reads the four rate tables for a tax year and assembles a RateTable.
// A missing tax year (no payroll_tax_years row / no thresholds) is a clear error so
// callers can tell the operator the year hasn't been seeded yet.
func LoadRateTable(ctx context.Context, q payrolldb.Querier, taxYearStart int32) (RateTable, error) {
	year, err := q.GetTaxYear(ctx, taxYearStart)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return RateTable{}, kernel.ErrValidation("payroll rates have not been configured for this tax year", nil)
		}
		return RateTable{}, kernel.ErrInternal(err)
	}

	thresholds, err := q.GetNiThresholds(ctx, taxYearStart)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return RateTable{}, kernel.ErrValidation("NI thresholds have not been configured for this tax year", nil)
		}
		return RateTable{}, kernel.ErrInternal(err)
	}

	bandRows, err := q.ListPayeBands(ctx, taxYearStart)
	if err != nil {
		return RateTable{}, kernel.ErrInternal(err)
	}
	catRows, err := q.ListNiCategoryRates(ctx, taxYearStart)
	if err != nil {
		return RateTable{}, kernel.ErrInternal(err)
	}

	rt := RateTable{
		TaxYearStart:                int(year.TaxYearStart),
		DefaultTaxCode:              year.DefaultTaxCode,
		PeriodCount:                 int(year.PeriodCount),
		EmploymentAllowanceCapMinor: year.EmploymentAllowanceCapMinor,
		PayeBands:                   make(map[string][]Band),
		NICategories:                make(map[string]NICategoryRate, len(catRows)),
		NI: NIThresholds{
			LELAnnualMinor: thresholds.LelAnnualMinor, LELMonthlyMinor: thresholds.LelMonthlyMinor,
			PTAnnualMinor: thresholds.PtAnnualMinor, PTMonthlyMinor: thresholds.PtMonthlyMinor,
			STAnnualMinor: thresholds.StAnnualMinor, STMonthlyMinor: thresholds.StMonthlyMinor,
			UELAnnualMinor: thresholds.UelAnnualMinor, UELMonthlyMinor: thresholds.UelMonthlyMinor,
		},
	}

	// Bands arrive ordered by (region, band_order) from the query, so appending
	// preserves the lowest-rate-first order within each region.
	for _, b := range bandRows {
		var upper int64
		if b.UpperThresholdMinor.Valid {
			upper = b.UpperThresholdMinor.Int64
		}
		rt.PayeBands[b.Region] = append(rt.PayeBands[b.Region], Band{UpperMinor: upper, RateBps: b.RateBps})
	}
	for _, c := range catRows {
		rt.NICategories[c.CategoryLetter] = NICategoryRate{
			EmployeeMainBps:  c.EmployeeMainBps,
			EmployeeUpperBps: c.EmployeeUpperBps,
			EmployerBps:      c.EmployerBps,
		}
	}
	return rt, nil
}
