package payroll

// dto.go
// =============================================================================
// Request/response shapes for the payroll endpoints (/api/v1/payroll*).
//
// Money crosses the API boundary as decimal POUND strings (e.g. "1047.00"), never
// pence integers or floats — converted with the money package. Dates are ISO
// YYYY-MM-DD. The computed figures (gross/tax/NI/net) and the year-to-date block are
// read-only output; only the input fields on UpdatePayslipRequest are writable.
// =============================================================================

// PreparePayRunRequest is the body for POST /api/v1/payroll/periods — prepare the
// draft payslips for one tax month. tax_year is optional (defaults to the current
// UK tax year); period is the tax month 1–12; payment_date must fall within it.
type PreparePayRunRequest struct {
	TaxYear     *int   `json:"tax_year"`
	Period      int    `json:"period" binding:"required,min=1,max=12"`
	PaymentDate string `json:"payment_date" binding:"required"` // YYYY-MM-DD
}

// UpdatePayslipRequest is the body for PUT /api/v1/payroll/payslips/:id — the Edit
// Payslip screen. Money fields are pound strings (blank = 0). The config fields
// override the snapshot for this payslip; the computed figures are recomputed on save.
type UpdatePayslipRequest struct {
	// Snapshot/config
	TaxCode                  *string `json:"tax_code"`
	NiCategoryLetter         string  `json:"ni_category_letter"`
	NicCalculation           string  `json:"nic_calculation"`
	Week1Month1Basis         bool    `json:"week1_month1_basis"`
	StudentLoanUndergraduate bool    `json:"student_loan_undergraduate"`
	StudentLoanPostgraduate  bool    `json:"student_loan_postgraduate"`

	// Pay
	BasicPay             string `json:"basic_pay"`
	Overtime             string `json:"overtime"`
	Bonus                string `json:"bonus"`
	Commission           string `json:"commission"`
	Allowance            string `json:"allowance"`
	AbsencePayments      string `json:"absence_payments"`
	HolidayPay           string `json:"holiday_pay"`
	OtherPayments        string `json:"other_payments"`
	PayNotSubjectToTaxNi string `json:"pay_not_subject_to_tax_ni"`

	// Statutory pay (entered, not auto-calculated in v1)
	StatutorySickPay                string `json:"statutory_sick_pay"`
	StatutoryMaternityPay           string `json:"statutory_maternity_pay"`
	StatutoryPaternityPay           string `json:"statutory_paternity_pay"`
	StatutoryAdoptionPay            string `json:"statutory_adoption_pay"`
	SharedParentalPay               string `json:"shared_parental_pay"`
	StatutoryNeonatalCarePay        string `json:"statutory_neonatal_care_pay"`
	StatutoryParentalBereavementPay string `json:"statutory_parental_bereavement_pay"`

	// Deductions
	PayrollGiving             string `json:"payroll_giving"`
	OtherDeductionsNetPay     string `json:"other_deductions_net_pay"`
	ItemsClass1NicNotPaye     string `json:"items_class1_nic_not_paye"`
	SalarySacrificeDeductions string `json:"salary_sacrifice_deductions"`

	HoursWorked *string `json:"hours_worked"`
	Comment     *string `json:"comment"`
}

// PayslipYTD is the year-to-date block shown on a payslip (cumulative through the
// payslip's period). All pound strings.
type PayslipYTD struct {
	GrossPay    string `json:"gross_pay"`
	TaxDeducted string `json:"tax_deducted"`
	EmployeeNI  string `json:"employee_ni"`
	EmployerNI  string `json:"employer_ni"`
	NetPay      string `json:"net_pay"`
}

// PayslipResponse is one employee's payslip. Input + computed money fields are pound
// strings. employee_name is composed for display.
type PayslipResponse struct {
	ID           string `json:"id"`
	PayRunID     string `json:"pay_run_id"`
	UserID       string `json:"user_id"`
	EmployeeName string `json:"employee_name"`

	TaxCode                  *string `json:"tax_code,omitempty"`
	NiCategoryLetter         string  `json:"ni_category_letter"`
	NicCalculation           string  `json:"nic_calculation"`
	Week1Month1Basis         bool    `json:"week1_month1_basis"`
	StudentLoanUndergraduate bool    `json:"student_loan_undergraduate"`
	StudentLoanPostgraduate  bool    `json:"student_loan_postgraduate"`

	// Pay inputs (pounds)
	BasicPay             string `json:"basic_pay"`
	Overtime             string `json:"overtime"`
	Bonus                string `json:"bonus"`
	Commission           string `json:"commission"`
	Allowance            string `json:"allowance"`
	AbsencePayments      string `json:"absence_payments"`
	HolidayPay           string `json:"holiday_pay"`
	OtherPayments        string `json:"other_payments"`
	PayNotSubjectToTaxNi string `json:"pay_not_subject_to_tax_ni"`

	// Statutory pay (pounds)
	StatutorySickPay                string `json:"statutory_sick_pay"`
	StatutoryMaternityPay           string `json:"statutory_maternity_pay"`
	StatutoryPaternityPay           string `json:"statutory_paternity_pay"`
	StatutoryAdoptionPay            string `json:"statutory_adoption_pay"`
	SharedParentalPay               string `json:"shared_parental_pay"`
	StatutoryNeonatalCarePay        string `json:"statutory_neonatal_care_pay"`
	StatutoryParentalBereavementPay string `json:"statutory_parental_bereavement_pay"`

	// Deductions (pounds)
	PayrollGiving             string `json:"payroll_giving"`
	OtherDeductionsNetPay     string `json:"other_deductions_net_pay"`
	ItemsClass1NicNotPaye     string `json:"items_class1_nic_not_paye"`
	SalarySacrificeDeductions string `json:"salary_sacrifice_deductions"`

	// Computed outputs (pounds)
	GrossPay        string `json:"gross_pay"`
	TaxablePay      string `json:"taxable_pay"`
	NiablePay       string `json:"niable_pay"`
	TaxDeducted     string `json:"tax_deducted"`
	EmployeeNI      string `json:"employee_ni"`
	EmployerNI      string `json:"employer_ni"`
	EmployeePension string `json:"employee_pension"`
	EmployerPension string `json:"employer_pension"`
	StudentLoan     string `json:"student_loan"`
	NetPay          string `json:"net_pay"`

	YearToDate PayslipYTD `json:"year_to_date"`

	HoursWorked    *string `json:"hours_worked,omitempty"`
	Comment        *string `json:"comment,omitempty"`
	LeavingPayslip bool    `json:"leaving_payslip"`
}

// PayRunTotals are the column totals for a run (the History row + the run footer).
type PayRunTotals struct {
	Pay        string `json:"pay"`
	Tax        string `json:"tax"`
	EmployeeNI string `json:"employee_ni"`
	EmployerNI string `json:"employer_ni"`
	NetPay     string `json:"net_pay"`
	DueToHmrc  string `json:"due_to_hmrc"`
}

// PayRunResponse is a pay run header + totals. Payslips is present only on the detail
// (GetPayRun / PreparePayRun / CompletePayRun) responses, omitted from the list.
type PayRunResponse struct {
	ID             string `json:"id"`
	OrganisationID string `json:"organisation_id"`
	TaxYearStart   int    `json:"tax_year_start"`
	Period         int    `json:"period"`

	PeriodStart string `json:"period_start"`
	PeriodEnd   string `json:"period_end"`
	PaymentDate string `json:"payment_date"`

	Status string `json:"status"`

	EmploymentAllowanceClaimed bool   `json:"employment_allowance_claimed"`
	EmploymentAllowanceAmount  string `json:"employment_allowance_amount"`

	Totals   PayRunTotals      `json:"totals"`
	Payslips []PayslipResponse `json:"payslips,omitempty"`

	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
}

// OverviewStatus is the overview's "Status" card.
type OverviewStatus struct {
	CurrentPeriod  int     `json:"current_period"` // the latest prepared period (0 = none yet)
	CurrentRunID   *string `json:"current_run_id,omitempty"`
	State          string  `json:"state"` // "none" | "draft" | "completed_unfiled"
	FilingDeadline *string `json:"filing_deadline,omitempty"`
	NextPeriod     *int    `json:"next_period,omitempty"` // next period available to prepare
	CanPrepare     bool    `json:"can_prepare"`
}

// OverviewYTD is the overview's "Year-to-date" card.
type OverviewYTD struct {
	TotalPay            string `json:"total_pay"`
	TotalTax            string `json:"total_tax"`
	TotalNI             string `json:"total_ni"`
	EmploymentAllowance string `json:"employment_allowance"`
}

// OverviewEmployee is one row in the overview's "Employees" table.
type OverviewEmployee struct {
	UserID        string  `json:"user_id"`
	Name          string  `json:"name"`
	StartDate     *string `json:"start_date,omitempty"`
	MonthlyPay    string  `json:"monthly_pay"`
	TotalPay      string  `json:"total_pay"` // YTD gross
	TotalTax      string  `json:"total_tax"` // YTD tax
	AutoEnrolment string  `json:"auto_enrolment"`
}

// OverviewResponse backs GET /api/v1/payroll/overview.
type OverviewResponse struct {
	TaxYear      int                `json:"tax_year"`
	TaxYearLabel string             `json:"tax_year_label"`
	CompanyName  string             `json:"company_name"`
	Status       OverviewStatus     `json:"status"`
	YearToDate   OverviewYTD        `json:"year_to_date"`
	History      []PayRunResponse   `json:"history"`
	Employees    []OverviewEmployee `json:"employees"`
}
