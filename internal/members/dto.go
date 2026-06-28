package members

// dto.go
// =============================================================================
// The response shape for the members endpoint (GET /api/v1/members), moved here
// from server.go with the handler.
// =============================================================================

// MemberResponse is the JSON returned for one organisation member (a membership
// joined to its user). It deliberately exposes only what a "Team / Manage users"
// screen needs — no password hash or other secrets. UUIDs are strings and
// timestamps are RFC3339; avatar_url and last_login_at are nullable (omitted when
// absent). role and status are the membership enum/status values, so the UI can
// badge each member.
type MemberResponse struct {
	MembershipID string  `json:"membership_id"`
	UserID       string  `json:"user_id"`
	Email        string  `json:"email"`
	FirstName    string  `json:"first_name"`
	LastName     string  `json:"last_name"`
	Role         string  `json:"role"`   // owner | admin | member | accountant | read_only
	Status       string  `json:"status"` // active | invited | suspended | deactivated
	AvatarURL    *string `json:"avatar_url,omitempty"`
	MemberSince  string  `json:"member_since"` // RFC3339 (membership created_at)
	LastLoginAt  *string `json:"last_login_at,omitempty"`
}

// MemberDetailResponse is the JSON for ONE member on the admin User Details screen
// (GET /api/v1/members/:id). It is the list shape plus the payroll-identity fields
// the detail form edits (national_insurance_number / utr / date_of_birth). Those
// come from the users row, which the list query doesn't select — hence a separate,
// richer response. Still no secrets (no password hash, tokens, last-login IP).
type MemberDetailResponse struct {
	MembershipID            string      `json:"membership_id"`
	UserID                  string      `json:"user_id"`
	Email                   string      `json:"email"`
	FirstName               string      `json:"first_name"`
	LastName                string      `json:"last_name"`
	Role                    string      `json:"role"`
	Status                  string      `json:"status"`
	AvatarURL               *string     `json:"avatar_url,omitempty"`
	NationalInsuranceNumber *string     `json:"national_insurance_number,omitempty"`
	UTR                     *string     `json:"utr,omitempty"`
	DateOfBirth             *string     `json:"date_of_birth,omitempty"` // ISO YYYY-MM-DD
	AddressLine1            *string     `json:"address_line_1,omitempty"`
	AddressLine2            *string     `json:"address_line_2,omitempty"`
	AddressLine3            *string     `json:"address_line_3,omitempty"`
	AddressLine4            *string     `json:"address_line_4,omitempty"`
	Postcode                *string     `json:"postcode,omitempty"`
	MemberSince             string      `json:"member_since"` // RFC3339 (membership created_at)
	LastLoginAt             *string     `json:"last_login_at,omitempty"`
	Payroll                 *PayrollDTO `json:"payroll,omitempty"` // always populated on GET (defaults if no row)
}

// PayrollDTO is the FreeAgent-style payroll employee-information for one membership,
// carried both ways (nested in MemberDetailResponse on GET and in UpdateMemberRequest
// on PUT). Owner/admin only. MONEY fields are decimal POUND strings ("700.00"); the
// service converts to/from BIGINT pence with the money package. Enums/booleans are the
// raw values (validated in the service); optional text/date fields are pointers
// (nil/blank -> NULL). The conditional detail behind Statutory Pay = Yes and Pension =
// "making contributions" is deferred (the UI disables those options).
type PayrollDTO struct {
	// Employment details
	IsExistingEmployee  bool    `json:"is_existing_employee"`
	StartDate           *string `json:"start_date"`           // ISO YYYY-MM-DD
	StartingDeclaration *string `json:"starting_declaration"` // A | B | C
	NicCalculation      string  `json:"nic_calculation"`      // director | director_alternative | employee
	NormalWorkingHours  *string `json:"normal_working_hours"` // under_16 | 16_to_24 | 24_to_30 | 30_plus | other
	PaidHourly          bool    `json:"paid_hourly"`
	PaidIrregularly     bool    `json:"paid_irregularly"`
	PayrollID           *string `json:"payroll_id"`

	// Tax and National Insurance
	TaxCode                  *string `json:"tax_code"`
	Week1Month1Basis         bool    `json:"week1_month1_basis"`
	NiCategoryLetter         string  `json:"ni_category_letter"`
	StudentLoanUndergraduate bool    `json:"student_loan_undergraduate"`
	StudentLoanPostgraduate  bool    `json:"student_loan_postgraduate"`

	// Monthly Pay — pound strings.
	BasicPay             string `json:"basic_pay"`
	Allowance            string `json:"allowance"`
	OtherPayments        string `json:"other_payments"`
	PayNotSubjectToTaxNi string `json:"pay_not_subject_to_tax_ni"`

	// Statutory Pay (top-level flag; amount detail deferred).
	ReceivingStatutoryPay bool `json:"receiving_statutory_pay"`

	// Monthly Deductions — pound strings.
	PayrollGiving             string `json:"payroll_giving"`
	OtherDeductionsNetPay     string `json:"other_deductions_net_pay"`
	ItemsClass1NicNotPaye     string `json:"items_class1_nic_not_paye"`
	SalarySacrificeDeductions string `json:"salary_sacrifice_deductions"`

	// Pension (status only; contribution-amount detail deferred).
	PensionStatus string `json:"pension_status"` // not_yet_eligible | opted_out_or_ineligible | making_contributions

	// Leaving details
	LeavingNextPayRun bool    `json:"leaving_next_pay_run"`
	LeavingDate       *string `json:"leaving_date"` // ISO YYYY-MM-DD; set when leaving = Yes
}

// UpdateMemberRequest is the body for PUT /api/v1/members/:id — an owner/admin
// editing another user's details, role and status. Names are required (NOT NULL
// columns); the payroll fields are optional pointers (blank/omitted -> NULL) and
// validated via the shared kernel.Parse* helpers. role/status carry oneof binding
// so an unknown value is a 400 at the edge; the service adds the cross-cutting
// guards (self lock-out, owner-only owner role). status excludes 'invited', which
// is owned by the (deferred) invite flow, not this form.
type UpdateMemberRequest struct {
	FirstName               string  `json:"first_name" binding:"required,max=100"`
	LastName                string  `json:"last_name" binding:"required,max=100"`
	NationalInsuranceNumber *string `json:"national_insurance_number"`
	UTR                     *string `json:"utr"`
	DateOfBirth             *string `json:"date_of_birth"` // ISO YYYY-MM-DD
	// Optional personal/home address — free text, blank/omitted -> NULL.
	AddressLine1 *string `json:"address_line_1"`
	AddressLine2 *string `json:"address_line_2"`
	AddressLine3 *string `json:"address_line_3"`
	AddressLine4 *string `json:"address_line_4"`
	Postcode     *string `json:"postcode"`
	Role         string  `json:"role" binding:"required,oneof=owner admin member accountant read_only"`
	Status       string  `json:"status" binding:"required,oneof=active suspended deactivated"`
	// Optional payroll block. When present, the payroll record is upserted in the
	// same transaction as the profile/role/status changes. Validated in the service
	// (money parse, enum membership) → 422.
	Payroll *PayrollDTO `json:"payroll"`
}
