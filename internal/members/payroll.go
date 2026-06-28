package members

// payroll.go
// =============================================================================
// Payroll DTO <-> DB conversion + validation for the admin User Details screen.
//
// The payroll block rides nested on GET/PUT /api/v1/members/:id (owner/admin only).
// On READ, payrollToDTO converts the stored row (pence, pgtype, enums) into the
// API shape (pound strings, plain/optional strings); a member with no row yet reads
// as defaults so the form always renders. On WRITE, buildPayrollParams validates and
// converts the inbound DTO into the sqlc upsert params, returning a 422
// (kernel.ErrValidation) for a bad money amount, an out-of-set enum, or a malformed
// date — BEFORE the transaction opens. Enum sets mirror the DB CHECK constraints.
// =============================================================================

import (
	"context"
	"errors"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	auth "github.com/operationfb/accounting-saas/db/auth"
	"github.com/operationfb/accounting-saas/internal/kernel"
	"github.com/operationfb/accounting-saas/money"
)

// Allowed enum values — mirror the CHECK constraints in employee_payroll.
var (
	nicCalculations      = []string{"director", "director_alternative", "employee"}
	workingHoursBands    = []string{"under_16", "16_to_24", "24_to_30", "30_plus", "other"}
	pensionStatuses      = []string{"not_yet_eligible", "opted_out_or_ineligible", "making_contributions"}
	startingDeclarations = []string{"A", "B", "C"}
	niCategoryLetters    = []string{"A", "B", "C", "F", "H", "I", "J", "L", "M", "N", "S", "V", "X", "Z"}
)

// loadPayrollDTO returns the payroll for (org, user) as an API DTO, or sensible
// defaults (zeros + the column-default enums) when no row exists yet.
func (s *Service) loadPayrollDTO(ctx context.Context, orgID, userID uuid.UUID) (*PayrollDTO, error) {
	ep, err := s.authQueries.GetEmployeePayroll(ctx, auth.GetEmployeePayrollParams{
		OrganisationID: orgID,
		UserID:         userID,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return defaultPayrollDTO(), nil
		}
		return nil, kernel.ErrInternal(err)
	}
	return payrollToDTO(ep), nil
}

// defaultPayrollDTO mirrors the table's column defaults, so a member with no saved
// payroll still renders a sensible blank form.
func defaultPayrollDTO() *PayrollDTO {
	const zero = "0.00"
	return &PayrollDTO{
		NicCalculation:            "employee",
		NiCategoryLetter:          "A",
		PensionStatus:             "opted_out_or_ineligible",
		BasicPay:                  zero,
		Allowance:                 zero,
		OtherPayments:             zero,
		PayNotSubjectToTaxNi:      zero,
		PayrollGiving:             zero,
		OtherDeductionsNetPay:     zero,
		ItemsClass1NicNotPaye:     zero,
		SalarySacrificeDeductions: zero,
	}
}

// payrollToDTO converts a stored row to the API shape: pence -> pound strings,
// pgtype -> plain/optional strings.
func payrollToDTO(ep auth.EmployeePayroll) *PayrollDTO {
	return &PayrollDTO{
		IsExistingEmployee:        ep.IsExistingEmployee,
		StartDate:                 kernel.DateToStringPtr(ep.StartDate),
		StartingDeclaration:       kernel.NullTextToPtr(ep.StartingDeclaration),
		NicCalculation:            ep.NicCalculation,
		NormalWorkingHours:        kernel.NullTextToPtr(ep.NormalWorkingHours),
		PaidHourly:                ep.PaidHourly,
		PaidIrregularly:           ep.PaidIrregularly,
		PayrollID:                 kernel.NullTextToPtr(ep.PayrollID),
		TaxCode:                   kernel.NullTextToPtr(ep.TaxCode),
		Week1Month1Basis:          ep.Week1Month1Basis,
		NiCategoryLetter:          ep.NiCategoryLetter,
		StudentLoanUndergraduate:  ep.StudentLoanUndergraduate,
		StudentLoanPostgraduate:   ep.StudentLoanPostgraduate,
		BasicPay:                  money.MinorToPounds(ep.BasicPayMinor),
		Allowance:                 money.MinorToPounds(ep.AllowanceMinor),
		OtherPayments:             money.MinorToPounds(ep.OtherPaymentsMinor),
		PayNotSubjectToTaxNi:      money.MinorToPounds(ep.PayNotSubjectToTaxNiMinor),
		ReceivingStatutoryPay:     ep.ReceivingStatutoryPay,
		PayrollGiving:             money.MinorToPounds(ep.PayrollGivingMinor),
		OtherDeductionsNetPay:     money.MinorToPounds(ep.OtherDeductionsNetPayMinor),
		ItemsClass1NicNotPaye:     money.MinorToPounds(ep.ItemsClass1NicNotPayeMinor),
		SalarySacrificeDeductions: money.MinorToPounds(ep.SalarySacrificeDeductionsMinor),
		PensionStatus:             ep.PensionStatus,
		LeavingNextPayRun:         ep.LeavingNextPayRun,
		LeavingDate:               kernel.DateToStringPtr(ep.LeavingDate),
	}
}

// buildPayrollParams validates + converts an inbound DTO into upsert params, mapping
// any bad value to a 422. Money is parsed pounds->pence (blank = 0); enums are checked
// against the allowed sets; the two dates allow future values (start/leaving).
func buildPayrollParams(orgID, userID uuid.UUID, p *PayrollDTO) (auth.UpsertEmployeePayrollParams, error) {
	out := auth.UpsertEmployeePayrollParams{
		OrganisationID:           orgID,
		UserID:                   userID,
		IsExistingEmployee:       p.IsExistingEmployee,
		PaidHourly:               p.PaidHourly,
		PaidIrregularly:          p.PaidIrregularly,
		Week1Month1Basis:         p.Week1Month1Basis,
		StudentLoanUndergraduate: p.StudentLoanUndergraduate,
		StudentLoanPostgraduate:  p.StudentLoanPostgraduate,
		ReceivingStatutoryPay:    p.ReceivingStatutoryPay,
		LeavingNextPayRun:        p.LeavingNextPayRun,
		PayrollID:                kernel.NullText(p.PayrollID),
		TaxCode:                  kernel.NullText(p.TaxCode),
	}

	// Dates (future allowed for start/leaving).
	var err error
	if out.StartDate, err = kernel.ParseDate(p.StartDate, "start date"); err != nil {
		return out, err
	}
	if out.LeavingDate, err = kernel.ParseDate(p.LeavingDate, "leaving date"); err != nil {
		return out, err
	}

	// Optional enums (nil/blank -> NULL).
	if out.StartingDeclaration, err = optionalEnum(p.StartingDeclaration, "starting declaration", startingDeclarations); err != nil {
		return out, err
	}
	if out.NormalWorkingHours, err = optionalEnum(p.NormalWorkingHours, "normal working hours", workingHoursBands); err != nil {
		return out, err
	}

	// Required enums (blank -> the column default).
	if out.NicCalculation, err = requiredEnum(p.NicCalculation, "employee", "NIC calculation", nicCalculations); err != nil {
		return out, err
	}
	if out.PensionStatus, err = requiredEnum(p.PensionStatus, "opted_out_or_ineligible", "pension status", pensionStatuses); err != nil {
		return out, err
	}
	if out.NiCategoryLetter, err = requiredEnum(p.NiCategoryLetter, "A", "NI category letter", niCategoryLetters); err != nil {
		return out, err
	}

	// Money: pounds -> pence (blank = 0).
	moneyFields := []struct {
		src  string
		dst  *int64
		name string
	}{
		{p.BasicPay, &out.BasicPayMinor, "basic pay"},
		{p.Allowance, &out.AllowanceMinor, "allowance"},
		{p.OtherPayments, &out.OtherPaymentsMinor, "other payments"},
		{p.PayNotSubjectToTaxNi, &out.PayNotSubjectToTaxNiMinor, "pay not subject to tax or NI"},
		{p.PayrollGiving, &out.PayrollGivingMinor, "payroll giving"},
		{p.OtherDeductionsNetPay, &out.OtherDeductionsNetPayMinor, "other deductions from net pay"},
		{p.ItemsClass1NicNotPaye, &out.ItemsClass1NicNotPayeMinor, "items subject to Class 1 NIC"},
		{p.SalarySacrificeDeductions, &out.SalarySacrificeDeductionsMinor, "salary sacrifice deductions"},
	}
	for _, m := range moneyFields {
		v, perr := parseMoney(m.src, m.name)
		if perr != nil {
			return out, perr
		}
		*m.dst = v
	}

	return out, nil
}

// parseMoney converts an optional pound string to integer pence; a blank value is 0,
// an unparseable one is a 422.
func parseMoney(raw, name string) (int64, error) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return 0, nil
	}
	minor, err := money.PoundsToMinor(s)
	if err != nil {
		return 0, kernel.ErrValidation(name+" must be a valid amount", err)
	}
	return minor, nil
}

// optionalEnum maps a nil/blank pointer to a NULL column; otherwise the value must be
// in the allowed set (else 422).
func optionalEnum(raw *string, name string, allowed []string) (pgtype.Text, error) {
	if raw == nil {
		return pgtype.Text{Valid: false}, nil
	}
	v := strings.TrimSpace(*raw)
	if v == "" {
		return pgtype.Text{Valid: false}, nil
	}
	if !contains(allowed, v) {
		return pgtype.Text{}, kernel.ErrValidation(name+" is not a valid option", nil)
	}
	return pgtype.Text{String: v, Valid: true}, nil
}

// requiredEnum defaults a blank value to def (the column default); a non-blank value
// must be in the allowed set (else 422).
func requiredEnum(raw, def, name string, allowed []string) (string, error) {
	v := strings.TrimSpace(raw)
	if v == "" {
		return def, nil
	}
	if !contains(allowed, v) {
		return "", kernel.ErrValidation(name+" is not a valid option", nil)
	}
	return v, nil
}

func contains(set []string, v string) bool {
	for _, s := range set {
		if s == v {
			return true
		}
	}
	return false
}
