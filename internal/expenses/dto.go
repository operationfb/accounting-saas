package expenses

// dto.go
// =============================================================================
// Request/response DTOs for the expenses domain (extracted from server.go).
//
// AttachmentResponse + AttachmentToResponse live HERE, not in the attachments
// package, to break the import cycle: the expense detail embeds the attachment
// list, and attachments depends one-way on this package (it returns these types
// from CaptureFromReceipt and the attachment CRUD handlers).
// =============================================================================

import (
	"encoding/json"
	"time"

	expensesdb "github.com/operationfb/accounting-saas/db/expenses"
	kernel "github.com/operationfb/accounting-saas/internal/kernel"
)

// CreateExpenseRequest is the JSON body accepted by POST /api/v1/expenses.
// Only the fields a client should supply are here. Internal fields (id,
// created_at, status, etc.) are set by the service, not the client.
type CreateExpenseRequest struct {
	CategoryID       string `json:"category_id"      binding:"required,uuid"`
	DatedOn          string `json:"dated_on"          binding:"required"` // YYYY-MM-DD
	Description      string `json:"description"       binding:"required,min=1"`
	CurrencyCode     string `json:"currency"          binding:"omitempty,len=3"` // defaults to GBP
	GrossValuePounds string `json:"gross_value"     binding:"required"`          // e.g. "42.50"

	// Optional fields — pointer types so we can distinguish "not provided"
	// from "provided as empty string / zero". A nil pointer means absent.
	ReceiptReference *string `json:"receipt_reference"`
	SupplierName     *string `json:"supplier_name"`
	SupplierVATNo    *string `json:"supplier_vat_number"`
	InvoiceNumber    *string `json:"invoice_number"`

	// Optional claimant. When an owner/admin records an expense on behalf of
	// someone else, this is that person's user id. Absent (nil) → the expense is
	// for the caller (the normal case). Authorised + validated in the service.
	UserID *string `json:"user_id" binding:"omitempty,uuid"`

	// VAT
	VATRateID *string `json:"vat_rate_id"` // UUID of the applicable VAT rate
	VATAmount *string `json:"vat_amount"`  // pounds, e.g. "3.33"; used only for non-fixed-ratio rates (ignored for fixed-ratio)

	// Project rebilling — all three must be provided together if rebilling
	ProjectID    *string `json:"project_id"`
	RebillType   *string `json:"rebill_type"`   // "cost" | "markup" | "price"
	RebillFactor *string `json:"rebill_factor"` // decimal string e.g. "1.15"
}

// UpdateExpenseRequest is the JSON body accepted by PUT /api/v1/expenses/:id.
// It mirrors CreateExpenseRequest's editable fields — PUT is a full replace of
// the editable representation. The claimant (user_id) and created_by are NOT
// editable and are never read from the body.
type UpdateExpenseRequest struct {
	CategoryID       string `json:"category_id"      binding:"required,uuid"`
	DatedOn          string `json:"dated_on"          binding:"required"` // YYYY-MM-DD
	Description      string `json:"description"       binding:"required,min=1"`
	CurrencyCode     string `json:"currency"          binding:"omitempty,len=3"` // defaults to GBP
	GrossValuePounds string `json:"gross_value"     binding:"required"`          // e.g. "42.50"

	// Optional fields — nil pointer means absent (omitted from the update body).
	ReceiptReference *string `json:"receipt_reference"`
	SupplierName     *string `json:"supplier_name"`
	SupplierVATNo    *string `json:"supplier_vat_number"`
	InvoiceNumber    *string `json:"invoice_number"`

	// VAT
	VATRateID *string `json:"vat_rate_id"`
	VATAmount *string `json:"vat_amount"` // pounds; used only for non-fixed-ratio rates (ignored for fixed-ratio)

	// Project rebilling
	ProjectID    *string `json:"project_id"`
	RebillType   *string `json:"rebill_type"`
	RebillFactor *string `json:"rebill_factor"`
}

// ChangeExpenseStatusRequest is the JSON body for POST /api/v1/expenses/:id/status.
// A single endpoint with an `action` discriminator drives the whole approval
// state machine (see expense_status.go) — one handler, the machine in one place.
//
// Validation is layered, like the contacts charge_vat field:
//   - binding here: `oneof` rejects an unknown action and `required_if` requires
//     a note when (and only when) rejecting (HTTP 400);
//   - the service re-checks both, independent of the HTTP layer (HTTP 422);
//   - the DB CHECK on expenses.status is the final backstop.
type ChangeExpenseStatusRequest struct {
	Action        string `json:"action"         binding:"required,oneof=submit approve reject reopen"`
	RejectionNote string `json:"rejection_note" binding:"required_if=Action reject"`
}

// ExpenseResponse is the JSON returned for a created or fetched expense.
// Amounts are returned as strings in pounds (e.g. "42.50") not raw pence,
// because JavaScript cannot safely represent large integers and clients
// should display formatted currency, not raw integers.
type ExpenseResponse struct {
	ID                string  `json:"id"`
	OrganisationID    string  `json:"organisation_id"`
	UserID            string  `json:"user_id"`
	CategoryID        string  `json:"category_id"`
	DatedOn           string  `json:"dated_on"`
	Description       string  `json:"description"`
	Currency          string  `json:"currency"`
	GrossValue        string  `json:"gross_value"`        // formatted pounds e.g. "42.50"
	NativeGrossValue  string  `json:"native_gross_value"` // in home currency
	VATValue          string  `json:"vat_value"`
	Status            string  `json:"status"`
	NeedsReview       bool    `json:"needs_review"` // TRUE while a Smart Upload capture awaits confirmation
	ReceiptReference  *string `json:"receipt_reference,omitempty"`
	SupplierName      *string `json:"supplier_name,omitempty"`
	SupplierVATNumber *string `json:"supplier_vat_number,omitempty"`
	InvoiceNumber     *string `json:"invoice_number,omitempty"`
	CreatedAt         string  `json:"created_at"`
	UpdatedAt         string  `json:"updated_at"`
}

// ExpenseDetailResponse is the richer JSON returned by GET /api/v1/expenses/:id.
// It comes from the v_expenses_full view (category + mileage joined) so a single
// call gives the detail screen everything: category name, VAT rate/status, FX /
// native values, EC status, project/rebill, and the approval timestamps. Money
// stays as pound strings; optional fields are omitted when null.
type ExpenseDetailResponse struct {
	ID          string `json:"id"`
	UserID      string `json:"user_id"` // claimant — raw FK for the edit form's "on behalf of" picker
	Status      string `json:"status"`
	DatedOn     string `json:"dated_on"`
	Description string `json:"description"`

	CategoryName        string `json:"category_name"`
	CategoryNominalCode string `json:"category_nominal_code"`
	CategoryID          string `json:"category_id"` // raw FK, for the edit form's picker

	Currency   string `json:"currency"`
	GrossValue string `json:"gross_value"`

	VATRateID *string `json:"vat_rate_id,omitempty"` // raw FK, for the edit form's picker
	VATRate   *string `json:"vat_rate,omitempty"`    // e.g. "20%"
	VATStatus string  `json:"vat_status"`
	VATValue  string  `json:"vat_value"`

	// Native / home-currency values — only differ from the above when the
	// expense was incurred in a foreign currency.
	NativeCurrency   string  `json:"native_currency"`
	NativeGrossValue string  `json:"native_gross_value"`
	NativeVATValue   string  `json:"native_vat_value"`
	ExchangeRate     *string `json:"exchange_rate,omitempty"`

	ECStatus string `json:"ec_status"`

	SupplierName      *string `json:"supplier_name,omitempty"`
	SupplierVATNumber *string `json:"supplier_vat_number,omitempty"`
	InvoiceNumber     *string `json:"invoice_number,omitempty"`
	ReceiptReference  *string `json:"receipt_reference,omitempty"`

	ProjectID    *string `json:"project_id,omitempty"`
	RebillType   *string `json:"rebill_type,omitempty"`
	RebillFactor *string `json:"rebill_factor,omitempty"`

	SubmittedAt      *string `json:"submitted_at,omitempty"`
	ApprovedAt       *string `json:"approved_at,omitempty"`
	ApprovedByUserID *string `json:"approved_by_user_id,omitempty"` // raw FK — who approved it
	PaidAt           *string `json:"paid_at,omitempty"`
	RejectionNote    *string `json:"rejection_note,omitempty"` // reason, set when REJECTED

	// Capture / OCR (Smart Upload). NeedsReview drives the review inbox;
	// OCRConfidence/OCRProcessedAt let the UI flag a low-confidence capture.
	// Attachments carries the file list — the frontend polls the primary
	// attachment's ocr_status here to know when OCR has filled the form.
	NeedsReview    bool                  `json:"needs_review"`
	OCRConfidence  *string               `json:"ocr_confidence,omitempty"`
	OCRProcessedAt *string               `json:"ocr_processed_at,omitempty"`
	Attachments    []*AttachmentResponse `json:"attachments"`

	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
}

// ExpenseCategoryResponse is the JSON returned for an expense category — the
// reference data the frontend uses to populate the category picker.
type ExpenseCategoryResponse struct {
	ID              string  `json:"id"`
	NominalCode     string  `json:"nominal_code"`
	Name            string  `json:"name"`
	CategoryGroup   *string `json:"category_group,omitempty"`
	Description     *string `json:"description,omitempty"`
	IsMileage       bool    `json:"is_mileage"`
	IsCapitalAsset  bool    `json:"is_capital_asset"`
	IsStockPurchase bool    `json:"is_stock_purchase"`
}

// VATRateResponse is the JSON returned for a VAT rate — reference data the
// frontend uses to populate the VAT rate picker and to compute VAT amounts.
//
// The rate is exposed two ways on purpose:
//   - rate_bps: the canonical integer (basis points, 2000 = 20.00%) the client
//     should use for EXACT computation (gross × rate_bps / 10000).
//   - rate: a ready-to-display string ("20%") so the dropdown doesn't have to
//     format it.
//
// is_fixed_ratio tells the client whether the VAT amount is locked to
// gross × rate (true) or whether the user may enter a custom amount (false).
type VATRateResponse struct {
	ID           string `json:"id"`
	Name         string `json:"name"`           // e.g. "Standard Rate"
	RateBps      int32  `json:"rate_bps"`       // basis points: 2000 = 20.00%
	Rate         string `json:"rate"`           // display form, e.g. "20%"
	IsFixedRatio bool   `json:"is_fixed_ratio"` // true = amount locked to gross × rate
}

// AttachmentResponse is the API shape for one attachment's metadata.
type AttachmentResponse struct {
	ID               string  `json:"id"`
	ExpenseID        string  `json:"expense_id"`
	FileName         string  `json:"file_name"`
	ContentType      string  `json:"content_type"`
	FileSizeBytes    int32   `json:"file_size_bytes"`
	IsPrimary        bool    `json:"is_primary"`
	Description      *string `json:"description,omitempty"`
	UploadedByUserID string  `json:"uploaded_by_user_id"`
	CreatedAt        string  `json:"created_at"`

	// OCR (Smart Upload). OCRStatus is the polled signal: PENDING|PROCESSING are
	// non-terminal; COMPLETE|FAILED|SKIPPED are terminal. OCRExtractedData is the
	// raw JSONB ("what OCR saw") passed straight through as JSON.
	OCRStatus        string          `json:"ocr_status"`
	OCRExtractedData json.RawMessage `json:"ocr_extracted_data,omitempty"`
	OCRProcessedAt   *string         `json:"ocr_processed_at,omitempty"`
}

// AttachmentToResponse maps a generated row to the API shape. It reuses the
// nullable-text/timestamp helpers from expense_service.go (same package).
func AttachmentToResponse(a expensesdb.ExpenseAttachment) *AttachmentResponse {
	return &AttachmentResponse{
		ID:               a.ID.String(),
		ExpenseID:        a.ExpenseID.String(),
		FileName:         a.FileName,
		ContentType:      a.ContentType,
		FileSizeBytes:    a.FileSizeBytes,
		IsPrimary:        a.IsPrimary,
		Description:      kernel.NullTextToPtr(a.Description),
		UploadedByUserID: a.UploadedByUserID.String(),
		CreatedAt:        a.CreatedAt.Time.Format(time.RFC3339),
		OCRStatus:        a.OcrStatus,
		// JSONB comes back as []byte; json.RawMessage passes it through verbatim.
		// nil (no OCR yet) is omitted by the ,omitempty tag.
		OCRExtractedData: json.RawMessage(a.OcrExtractedData),
		OCRProcessedAt:   kernel.TimestampToStringPtr(a.OcrProcessedAt),
	}
}
