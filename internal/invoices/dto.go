package invoices

// dto.go
// =============================================================================
// Request/response shapes for the invoices endpoints (/api/v1/invoices*).
//
// Money crosses the API boundary as decimal POUND strings (e.g. "42.50"), never
// pence integers or floats — converted with the money package. VAT rates cross as
// plain PERCENTAGE strings (e.g. "20", "17.5"), converted with money.PercentToBps /
// money.BpsToPercentString.
// =============================================================================

// InvoiceItemRequest is one line on a create/update payload. price is the per-unit
// price in POUNDS (VAT-exclusive); quantity is a decimal string (e.g. "2.5");
// sales_tax_rate is a percentage (omitted → 0, i.e. zero-rated).
type InvoiceItemRequest struct {
	Description  string `json:"description" binding:"required"`
	Quantity     string `json:"quantity" binding:"required"`
	Price        string `json:"price" binding:"required"`
	SalesTaxRate string `json:"sales_tax_rate"`
}

// CreateInvoiceRequest is the body for POST /api/v1/invoices. The owning org +
// creator come from the token, never the body. Line items are embedded.
type CreateInvoiceRequest struct {
	ContactID string               `json:"contact_id" binding:"required,uuid"`
	DatedOn   string               `json:"dated_on" binding:"required"` // YYYY-MM-DD
	DueOn     *string              `json:"due_on"`                      // YYYY-MM-DD, optional
	Reference string               `json:"reference" binding:"required"`
	Currency  string               `json:"currency" binding:"omitempty,len=3"` // ISO 4217; default GBP
	Items     []InvoiceItemRequest `json:"items"`
}

// UpdateInvoiceRequest is the body for PUT /api/v1/invoices/:id. PUT is a full
// replace of the editable representation INCLUDING the line items (which are
// rebuilt). Allowed only while the invoice is DRAFT.
type UpdateInvoiceRequest struct {
	ContactID string               `json:"contact_id" binding:"required,uuid"`
	DatedOn   string               `json:"dated_on" binding:"required"`
	DueOn     *string              `json:"due_on"`
	Reference string               `json:"reference" binding:"required"`
	Currency  string               `json:"currency" binding:"omitempty,len=3"`
	Items     []InvoiceItemRequest `json:"items"`
}

// ChangeStatusRequest is the body for POST /api/v1/invoices/:id/status. The action
// list MUST match the keys of invoiceActions in status.go (struct tags can't
// reference the map, so keep them in step).
type ChangeStatusRequest struct {
	Action string `json:"action" binding:"required,oneof=issue schedule send write_off refund reopen"`
}

// InvoiceItemResponse is one line in an invoice detail response. The per-line
// amounts (net/VAT/total, in pounds) are DERIVED by the service from the stored
// quantity, unit price and VAT rate — they are not stored columns.
type InvoiceItemResponse struct {
	ID            string `json:"id"`
	Position      int32  `json:"position"`
	Description   string `json:"description"`
	Quantity      string `json:"quantity"`
	Price         string `json:"price"`          // per-unit, pounds
	SalesTaxRate  string `json:"sales_tax_rate"` // percent
	NetValue      string `json:"net_value"`      // quantity × price, pounds
	SalesTaxValue string `json:"sales_tax_value"`
	TotalValue    string `json:"total_value"`
}

// InvoiceResponse is the JSON for a created/fetched/updated/listed invoice. Money
// fields are pound strings. status is the STORED lifecycle; display_status is the
// DERIVED presentation. items is present only on the DETAIL responses (get / create
// / update); it is omitted from the list.
type InvoiceResponse struct {
	ID              string `json:"id"`
	OrganisationID  string `json:"organisation_id"`
	CreatedByUserID string `json:"created_by_user_id"`
	ContactID       string `json:"contact_id"`

	DatedOn   string  `json:"dated_on"`
	DueOn     *string `json:"due_on,omitempty"`
	Reference *string `json:"reference,omitempty"`
	Currency  string  `json:"currency"`

	Status        string `json:"status"`         // stored lifecycle (DRAFT/SCHEDULED/SENT/WRITTEN_OFF/REFUNDED)
	DisplayStatus string `json:"display_status"` // derived (Draft/Scheduled/Open/Overdue/Paid/Overpaid/Zero Value/...)

	NetValue      string `json:"net_value"`
	SalesTaxValue string `json:"sales_tax_value"`
	TotalValue    string `json:"total_value"`
	PaidValue     string `json:"paid_value"`
	DueValue      string `json:"due_value"`

	Items []InvoiceItemResponse `json:"items,omitempty"`

	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
}
