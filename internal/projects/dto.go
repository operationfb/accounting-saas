package projects

// dto.go
// =============================================================================
// The request/response shapes for the projects endpoints (/api/v1/projects*),
// moved here from server.go with the handlers.
// =============================================================================

// CreateProjectRequest is the JSON body accepted by POST /api/v1/projects.
// Money amounts (billing_rate, budget_money) are decimal pound strings for
// precision. Time values (hours_per_day, budget_hours) are "H:MM" strings.
type CreateProjectRequest struct {
	// Core fields
	ContactID              string  `json:"contact_id" binding:"required,uuid"`
	Name                   string  `json:"name" binding:"required,min=1"`
	Status                 string  `json:"status"` // defaults to "active"
	ContractPONumber       *string `json:"contract_po_number"`
	ProjectInvoiceSequence bool    `json:"project_invoice_sequence"`

	// Time and money
	Currency           string  `json:"currency"`          // defaults to "GBP"
	BudgetType         *string `json:"budget_type"`       // "hours" | "days" | "money" | nil
	BudgetHours        *string `json:"budget_hours"`      // "H:MM" — used when budget_type="hours"
	BudgetDays         *int32  `json:"budget_days"`       // integer — used when budget_type="days"
	BudgetMoney        *string `json:"budget_money"`      // pound string — used when budget_type="money"
	HoursPerDay        *string `json:"hours_per_day"`     // "H:MM" e.g. "8:00"
	BillingRate        string  `json:"billing_rate"`      // pound string e.g. "100.00"
	BillingRateUnit    *string `json:"billing_rate_unit"` // "per_hour" | "per_day"
	BillingRatePlusVAT bool    `json:"billing_rate_plus_vat"`

	// More options
	IsIR35                bool    `json:"is_ir35"`
	StartDate             *string `json:"start_date"` // "YYYY-MM-DD"
	EndDate               *string `json:"end_date"`   // "YYYY-MM-DD"
	IncludeUnbillableTime bool    `json:"include_unbillable_time"`
}

// UpdateProjectRequest mirrors CreateProjectRequest — PUT replaces all editable
// fields. contact_id is required so the project can be re-linked on edit.
type UpdateProjectRequest struct {
	ContactID              string  `json:"contact_id" binding:"required,uuid"`
	Name                   string  `json:"name" binding:"required,min=1"`
	Status                 string  `json:"status"`
	ContractPONumber       *string `json:"contract_po_number"`
	ProjectInvoiceSequence bool    `json:"project_invoice_sequence"`
	Currency               string  `json:"currency"`
	BudgetType             *string `json:"budget_type"`
	BudgetHours            *string `json:"budget_hours"`
	BudgetDays             *int32  `json:"budget_days"`
	BudgetMoney            *string `json:"budget_money"`
	HoursPerDay            *string `json:"hours_per_day"`
	BillingRate            string  `json:"billing_rate"`
	BillingRateUnit        *string `json:"billing_rate_unit"`
	BillingRatePlusVAT     bool    `json:"billing_rate_plus_vat"`
	IsIR35                 bool    `json:"is_ir35"`
	StartDate              *string `json:"start_date"`
	EndDate                *string `json:"end_date"`
	IncludeUnbillableTime  bool    `json:"include_unbillable_time"`
}

// ProjectResponse is the JSON returned for a created/fetched/updated project.
// Internal fields (pgx types, raw pence) are converted to human-readable form.
type ProjectResponse struct {
	ID                     string  `json:"id"`
	OrganisationID         string  `json:"organisation_id"`
	ContactID              string  `json:"contact_id"`
	Name                   string  `json:"name"`
	Status                 string  `json:"status"`
	ContractPONumber       *string `json:"contract_po_number"`
	ProjectInvoiceSequence bool    `json:"project_invoice_sequence"`
	Currency               string  `json:"currency"`
	BudgetType             *string `json:"budget_type"`
	BudgetHours            *string `json:"budget_hours"`  // "H:MM" when budget_type="hours"
	BudgetDays             *int32  `json:"budget_days"`   // integer when budget_type="days"
	BudgetMoney            *string `json:"budget_money"`  // pounds when budget_type="money"
	HoursPerDay            *string `json:"hours_per_day"` // "H:MM"
	BillingRate            string  `json:"billing_rate"`  // pound string
	BillingRateUnit        *string `json:"billing_rate_unit"`
	BillingRatePlusVAT     bool    `json:"billing_rate_plus_vat"`
	IsIR35                 bool    `json:"is_ir35"`
	StartDate              *string `json:"start_date"`
	EndDate                *string `json:"end_date"`
	IncludeUnbillableTime  bool    `json:"include_unbillable_time"`
	CreatedAt              string  `json:"created_at"`
	UpdatedAt              string  `json:"updated_at"`
}
