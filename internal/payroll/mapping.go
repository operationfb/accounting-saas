package payroll

// mapping.go
// =============================================================================
// Small conversion helpers shared by the service: pgtype <-> Go, the payslip
// input-param builder (pounds → pence + enum validation), and the pay-component
// extraction the engine consumes.
// =============================================================================

import (
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	payrolldb "github.com/operationfb/accounting-saas/db/payroll"
	"github.com/operationfb/accounting-saas/internal/kernel"
	"github.com/operationfb/accounting-saas/money"
)

// nicCalculations mirrors the employee_payroll CHECK constraint.
var nicCalculations = []string{"director", "director_alternative", "employee"}

// inputsFromPayslip extracts the engine inputs from a stored payslip row.
func inputsFromPayslip(ps payrolldb.Payslip) PayslipInputs {
	return PayslipInputs{
		BasicPay:             ps.BasicPayMinor,
		Overtime:             ps.OvertimeMinor,
		Bonus:                ps.BonusMinor,
		Commission:           ps.CommissionMinor,
		Allowance:            ps.AllowanceMinor,
		AbsencePayments:      ps.AbsencePaymentsMinor,
		HolidayPay:           ps.HolidayPayMinor,
		OtherPayments:        ps.OtherPaymentsMinor,
		PayNotSubjectToTaxNI: ps.PayNotSubjectToTaxNiMinor,

		StatutorySick:                ps.StatutorySickPayMinor,
		StatutoryMaternity:           ps.StatutoryMaternityPayMinor,
		StatutoryPaternity:           ps.StatutoryPaternityPayMinor,
		StatutoryAdoption:            ps.StatutoryAdoptionPayMinor,
		SharedParental:               ps.SharedParentalPayMinor,
		StatutoryNeonatal:            ps.StatutoryNeonatalCarePayMinor,
		StatutoryParentalBereavement: ps.StatutoryParentalBereavementPayMinor,

		PayrollGiving:         ps.PayrollGivingMinor,
		OtherDeductionsNetPay: ps.OtherDeductionsNetPayMinor,
		ItemsClass1NicNotPaye: ps.ItemsClass1NicNotPayeMinor,
		SalarySacrifice:       ps.SalarySacrificeDeductionsMinor,
	}
}

// buildPayslipInputParams validates + converts an Edit Payslip request into the
// sqlc update params, returning a 422 for a bad amount or enum BEFORE the
// transaction opens.
func buildPayslipInputParams(psID, orgID uuid.UUID, req UpdatePayslipRequest) (payrolldb.UpdatePayslipInputsParams, error) {
	out := payrolldb.UpdatePayslipInputsParams{
		ID:                       psID,
		OrganisationID:           orgID,
		Week1Month1Basis:         req.Week1Month1Basis,
		StudentLoanUndergraduate: req.StudentLoanUndergraduate,
		StudentLoanPostgraduate:  req.StudentLoanPostgraduate,
		TaxCode:                  kernel.NullText(req.TaxCode),
		Comment:                  kernel.NullText(req.Comment),
	}

	// NIC calculation: blank → the employee default; else must be in the set.
	nic := strings.TrimSpace(req.NicCalculation)
	if nic == "" {
		nic = "employee"
	} else if !contains(nicCalculations, nic) {
		return out, kernel.ErrValidation("nic_calculation is not a valid option", nil)
	}
	out.NicCalculation = nic

	// NI category: blank → A. (The seeded-rate check happens in the engine.)
	cat := strings.ToUpper(strings.TrimSpace(req.NiCategoryLetter))
	if cat == "" {
		cat = "A"
	}
	out.NiCategoryLetter = cat

	// Hours worked (optional decimal).
	if req.HoursWorked != nil && strings.TrimSpace(*req.HoursWorked) != "" {
		var n pgtype.Numeric
		if err := n.Scan(strings.TrimSpace(*req.HoursWorked)); err != nil {
			return out, kernel.ErrValidation("hours_worked must be a valid number", err)
		}
		out.HoursWorked = n
	}

	// Money fields (pounds → pence; blank = 0).
	fields := []struct {
		src string
		dst *int64
		nm  string
	}{
		{req.BasicPay, &out.BasicPayMinor, "basic pay"},
		{req.Overtime, &out.OvertimeMinor, "overtime"},
		{req.Bonus, &out.BonusMinor, "bonus"},
		{req.Commission, &out.CommissionMinor, "commission"},
		{req.Allowance, &out.AllowanceMinor, "allowance"},
		{req.AbsencePayments, &out.AbsencePaymentsMinor, "absence payments"},
		{req.HolidayPay, &out.HolidayPayMinor, "holiday pay"},
		{req.OtherPayments, &out.OtherPaymentsMinor, "other payments"},
		{req.PayNotSubjectToTaxNi, &out.PayNotSubjectToTaxNiMinor, "pay not subject to tax or NI"},
		{req.StatutorySickPay, &out.StatutorySickPayMinor, "statutory sick pay"},
		{req.StatutoryMaternityPay, &out.StatutoryMaternityPayMinor, "statutory maternity pay"},
		{req.StatutoryPaternityPay, &out.StatutoryPaternityPayMinor, "statutory paternity pay"},
		{req.StatutoryAdoptionPay, &out.StatutoryAdoptionPayMinor, "statutory adoption pay"},
		{req.SharedParentalPay, &out.SharedParentalPayMinor, "shared parental pay"},
		{req.StatutoryNeonatalCarePay, &out.StatutoryNeonatalCarePayMinor, "statutory neonatal care pay"},
		{req.StatutoryParentalBereavementPay, &out.StatutoryParentalBereavementPayMinor, "statutory parental bereavement pay"},
		{req.PayrollGiving, &out.PayrollGivingMinor, "payroll giving"},
		{req.OtherDeductionsNetPay, &out.OtherDeductionsNetPayMinor, "other deductions from net pay"},
		{req.ItemsClass1NicNotPaye, &out.ItemsClass1NicNotPayeMinor, "items subject to Class 1 NIC"},
		{req.SalarySacrificeDeductions, &out.SalarySacrificeDeductionsMinor, "salary sacrifice deductions"},
	}
	for _, f := range fields {
		v, err := parsePounds(f.src, f.nm)
		if err != nil {
			return out, err
		}
		*f.dst = v
	}
	return out, nil
}

// parsePounds converts an optional pound string to pence; blank = 0, bad = 422.
func parsePounds(raw, name string) (int64, error) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return 0, nil
	}
	v, err := money.PoundsToMinor(s)
	if err != nil {
		return 0, kernel.ErrValidation(name+" must be a valid amount", err)
	}
	return v, nil
}

// taxCodeOf returns the stored tax code string ("" when NULL — the engine then uses
// the year's default).
func taxCodeOf(t pgtype.Text) string {
	if !t.Valid {
		return ""
	}
	return t.String
}

// pensionLabel maps the stored pension_status enum to the overview's display text.
func pensionLabel(status string) string {
	switch status {
	case "making_contributions":
		return "Making contributions"
	case "not_yet_eligible":
		return "Not yet eligible"
	default:
		return "Opted out or ineligible"
	}
}

// parseDate parses a required YYYY-MM-DD string into a pgtype.Date.
func parseDate(field, s string) (pgtype.Date, error) {
	t, err := time.Parse("2006-01-02", strings.TrimSpace(s))
	if err != nil {
		return pgtype.Date{}, kernel.ErrValidation(field+" must be a valid date in YYYY-MM-DD format", err)
	}
	return pgtype.Date{Time: t, Valid: true}, nil
}

// pgDate wraps a time.Time as a valid pgtype.Date.
func pgDate(t time.Time) pgtype.Date {
	return pgtype.Date{Time: t, Valid: true}
}

// dateToStringPtr renders a nullable date as a YYYY-MM-DD *string; nil when NULL.
func dateToStringPtr(d pgtype.Date) *string {
	if !d.Valid {
		return nil
	}
	s := d.Time.Format("2006-01-02")
	return &s
}

// numericToStringPtr renders a nullable NUMERIC as a *string; nil when NULL.
func numericToStringPtr(n pgtype.Numeric) *string {
	if !n.Valid {
		return nil
	}
	v, err := n.Value()
	if err != nil {
		return nil
	}
	s, ok := v.(string)
	if !ok {
		return nil
	}
	return &s
}

// mapExec maps a bare exec error to an AppError (500) or nil.
func mapExec(err error) error {
	if err != nil {
		return kernel.ErrInternal(err)
	}
	return nil
}

func contains(set []string, v string) bool {
	for _, s := range set {
		if s == v {
			return true
		}
	}
	return false
}
