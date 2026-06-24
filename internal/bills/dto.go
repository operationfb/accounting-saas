package bills

// dto.go
// =============================================================================
// Request/response shapes for the bills endpoints (/api/v1/bills*).
//
// Money crosses the API boundary as decimal POUND strings (e.g. "42.50"), never
// pence integers or floats — converted with the money package.
//
// VAT mirrors the EXPENSES pattern (not the invoices percentage one): the amount
// entered is the VAT-INCLUSIVE total, and the client picks a vat_rate_id. A
// fixed-ratio rate has its VAT extracted from the total; a non-fixed-ratio
// ("manual") rate takes the VAT from vat_amount (e.g. a Smart-Upload figure).
// There is no Including/Excluding-VAT radio and no "Auto".
// =============================================================================

// CreateBillRequest is the body for POST /api/v1/bills. The owning org + creator
// come from the token, never the body.
type CreateBillRequest struct {
	ContactID      string  `json:"contact_id" binding:"required,uuid"`
	Reference      string  `json:"reference" binding:"required"`        // the SUPPLIER's invoice number
	DatedOn        string  `json:"dated_on" binding:"required"`         // YYYY-MM-DD ("Bill Date")
	DueOn          *string `json:"due_on"`                              // YYYY-MM-DD, optional ("Due On")
	Currency       string  `json:"currency" binding:"omitempty,len=3"`  // ISO 4217; default GBP
	Comments       *string `json:"comments"`                            // free-text
	IsHirePurchase bool    `json:"is_hire_purchase"`

	CategoryID string  `json:"category_id" binding:"required,uuid"`  // a CoA spending account
	Total      string  `json:"total" binding:"required"`             // the VAT-INCLUSIVE "Total Price", pounds
	VATRateID  *string `json:"vat_rate_id" binding:"omitempty,uuid"` // a vat_rates row; omitted → no VAT
	VATAmount  *string `json:"vat_amount"`                           // pounds; only used for a non-fixed-ratio (manual) rate
	ProjectID  *string `json:"project_id" binding:"omitempty,uuid"`  // optional "Link to Project"
}

// UpdateBillRequest is the body for PUT /api/v1/bills/:id — a full replace of the
// editable representation. Same shape as create (kept a distinct type so the two
// can diverge later, as in the invoices module).
type UpdateBillRequest struct {
	ContactID      string  `json:"contact_id" binding:"required,uuid"`
	Reference      string  `json:"reference" binding:"required"`
	DatedOn        string  `json:"dated_on" binding:"required"`
	DueOn          *string `json:"due_on"`
	Currency       string  `json:"currency" binding:"omitempty,len=3"`
	Comments       *string `json:"comments"`
	IsHirePurchase bool    `json:"is_hire_purchase"`

	CategoryID string  `json:"category_id" binding:"required,uuid"`
	Total      string  `json:"total" binding:"required"`
	VATRateID  *string `json:"vat_rate_id" binding:"omitempty,uuid"`
	VATAmount  *string `json:"vat_amount"`
	ProjectID  *string `json:"project_id" binding:"omitempty,uuid"`
}

// BillResponse is the JSON for a created/fetched/updated/listed bill. Money fields
// are pound strings. There is no stored status; display_status is DERIVED at read
// time from paid/total/due_on (a label, not a lifecycle).
type BillResponse struct {
	ID              string `json:"id"`
	OrganisationID  string `json:"organisation_id"`
	CreatedByUserID string `json:"created_by_user_id"`
	ContactID       string `json:"contact_id"`

	Reference      *string `json:"reference,omitempty"`
	DatedOn        string  `json:"dated_on"`
	DueOn          *string `json:"due_on,omitempty"`
	Currency       string  `json:"currency"`
	Comments       *string `json:"comments,omitempty"`
	IsHirePurchase bool    `json:"is_hire_purchase"`

	CategoryID string  `json:"category_id"`
	VATRateID  *string `json:"vat_rate_id,omitempty"`
	VATRate    string  `json:"vat_rate"` // display percent, e.g. "20%" ("" = no rate selected)

	NetValue      string `json:"net_value"`
	SalesTaxValue string `json:"sales_tax_value"`
	TotalValue    string `json:"total_value"`
	PaidValue     string `json:"paid_value"`
	DueValue      string `json:"due_value"`

	DisplayStatus string `json:"display_status"` // derived: Unpaid / Part paid / Paid / Overdue / Zero Value

	ProjectID *string `json:"project_id,omitempty"`

	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
}

// BillCategoryResponse is one row of the "Spending Category" picker (GET
// /api/v1/bill-categories). default_vat lets the SPA pre-select a sensible VAT rate.
type BillCategoryResponse struct {
	ID          string  `json:"id"`
	NominalCode string  `json:"nominal_code"`
	Name        string  `json:"name"`
	AccountType string  `json:"account_type"`
	ApiGroup    *string `json:"api_group,omitempty"`
	DefaultVat  *string `json:"default_vat,omitempty"`
}
