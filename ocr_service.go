package main

// ocr_service.go
// =============================================================================
// OcrService — reads a receipt/invoice with Google Document AI and writes the
// extracted fields back onto the expense, in the background.
//
// Where this sits in the "Smart Upload" flow:
//
//   AttachmentService.CaptureFromReceipt
//        creates a skeleton expense (needs_review=TRUE) + an attachment
//        (ocr_status=PENDING), then calls →  OcrService.Enqueue(...)
//                                                  │ spawns a goroutine
//                                                  ▼
//   OcrService.process:  PENDING → PROCESSING → (extract) → COMPLETE / FAILED / SKIPPED
//        On COMPLETE it stores the raw text + structured JSON on the attachment
//        AND fills the *empty* fields of the parent expense (never overwriting
//        anything a human typed). It deliberately leaves needs_review alone:
//        OCR finishing is not the same as a person confirming the figures.
//
// Why background (not synchronous)? The call to Document AI takes seconds; we
// don't want the upload request blocked on it. The frontend polls the expense
// and shows "Reading receipt…" until ocr_status reaches a terminal state.
//
// The actual Document AI call is hidden behind the DocumentExtractor interface
// (implemented by documentAIExtractor in ocr_documentai.go), exactly like
// Storage hides GCS. That keeps this orchestration testable without hitting the
// paid API — tests inject a fake extractor.
// =============================================================================

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/shopspring/decimal"

	expenses "github.com/operationfb/accounting-saas/db/expenses"
)

// Document types the "Smart Upload" toggle offers. Each routes to a different
// Document AI processor (see documentAIExtractor):
//   - receipt → the Expense Parser  (photographed POS/till receipts)
//   - invoice → the Invoice Parser  (PDF supplier invoices/bills; richer fields,
//     including the supplier VAT number we need for UK VAT reclaim)
const (
	DocumentTypeReceipt = "receipt"
	DocumentTypeInvoice = "invoice"
)

// validDocumentType reports whether t is one of the supported toggle values.
func validDocumentType(t string) bool {
	return t == DocumentTypeReceipt || t == DocumentTypeInvoice
}

const (
	// defaultOCRTimeout bounds a single background extraction. Document AI online
	// processing is usually a few seconds; 120s is generous headroom.
	defaultOCRTimeout = 120 * time.Second

	// ocrMaxBytes guards the in-memory read of a receipt before sending it to
	// Document AI. It matches the attachment upload cap (20 MiB); anything larger
	// is marked SKIPPED rather than processed.
	ocrMaxBytes int64 = 20 * 1024 * 1024
)

// =============================================================================
// EXTRACTION CONTRACT
// =============================================================================

// ExtractionResult is the normalised output of OCR — the same shape regardless
// of which Document AI processor produced it. Money is already in **minor units
// (pence) of Currency**; the extractor does the MoneyValue→pence conversion so
// this service never touches floats. Pointer fields are nil when the document
// did not yield that field (e.g. a till receipt rarely has a VAT number).
type ExtractionResult struct {
	RawText       string          // full text, stored in ocr_raw_text
	SupplierName  *string         // → expenses.supplier_name
	SupplierVAT   *string         // → expenses.supplier_vat_number (invoice parser only)
	InvoiceNumber *string         // → expenses.invoice_number
	Description   *string         // → expenses.description (assembled from supplier + line items)
	Currency      *string         // ISO 4217, e.g. "GBP"
	Date          *time.Time      // → expenses.dated_on
	TotalMinor    *int64          // gross (VAT-inclusive) total, minor units of Currency
	VATMinor      *int64          // tax amount, minor units of Currency
	Confidence    decimal.Decimal // mean entity confidence, 0..1 → expenses.ocr_confidence
}

// DocumentExtractor turns raw file bytes into an ExtractionResult. The real
// implementation calls Document AI (ocr_documentai.go); tests use a fake. The
// documentType selects the processor ("receipt" vs "invoice").
type DocumentExtractor interface {
	Extract(ctx context.Context, content []byte, contentType, documentType string) (*ExtractionResult, error)
}

// =============================================================================
// SERVICE
// =============================================================================

// OcrService owns the background extraction pipeline. It needs the pool (for the
// result-writing transaction), the queries (status/expense updates), the Storage
// (to re-read the file bytes), and a DocumentExtractor (the OCR engine).
type OcrService struct {
	pool      *pgxpool.Pool
	queries   *expenses.Queries
	storage   Storage
	extractor DocumentExtractor
	timeout   time.Duration
	maxBytes  int64 // files larger than this are SKIPPED (defaults to ocrMaxBytes)
}

// NewOcrService constructs the service. Both storage and extractor are required
// for OCR to actually run; main.go only wires this in when both are configured.
func NewOcrService(pool *pgxpool.Pool, queries *expenses.Queries, store Storage, extractor DocumentExtractor) *OcrService {
	return &OcrService{
		pool:      pool,
		queries:   queries,
		storage:   store,
		extractor: extractor,
		timeout:   defaultOCRTimeout,
		maxBytes:  ocrMaxBytes,
	}
}

// Enqueue kicks off OCR for one attachment in the BACKGROUND and returns
// immediately, so the upload request is never blocked on the (seconds-long) call
// to Document AI. The attachment already exists with ocr_status='PENDING'.
//
// This is fire-and-forget by design (see the plan's async caveats): there is no
// retry in v1, and a process crash mid-OCR would leave the row stuck in
// PROCESSING (the frontend's poll has a bounded give-up for that case). We use a
// fresh background context with a bounded timeout because the request's context
// ends the moment the handler returns.
func (s *OcrService) Enqueue(attachmentID, orgID uuid.UUID, documentType string) {
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), s.timeout)
		defer cancel()
		if err := s.process(ctx, attachmentID, orgID, documentType); err != nil {
			// Best-effort: log and move on. The attachment's ocr_status column is
			// the durable record of the outcome.
			log.Printf("ocr: attachment %s (%s): %v", attachmentID, documentType, err)
		}
	}()
}

// process runs the full PENDING→terminal pipeline for one attachment.
func (s *OcrService) process(ctx context.Context, attachmentID, orgID uuid.UUID, documentType string) error {
	// 1) Flip PENDING → PROCESSING so the polling frontend can show "Reading…".
	if err := s.queries.MarkAttachmentOCRProcessing(ctx, expenses.MarkAttachmentOCRProcessingParams{
		ID:             attachmentID,
		OrganisationID: orgID,
	}); err != nil {
		return fmt.Errorf("mark processing: %w", err)
	}

	// 2) Load the attachment metadata (storage key, content type, parent expense).
	att, err := s.queries.GetExpenseAttachment(ctx, expenses.GetExpenseAttachmentParams{
		ID:             attachmentID,
		OrganisationID: orgID,
	})
	if err != nil {
		s.markTerminal(ctx, attachmentID, orgID, "FAILED", "", nil)
		return fmt.Errorf("load attachment: %w", err)
	}

	// 3) Guard: too large to process inline → SKIPPED (a valid terminal state, not
	//    a failure). Keeps us inside Document AI's online-processing limits.
	if int64(att.FileSizeBytes) > s.maxBytes {
		s.markTerminal(ctx, attachmentID, orgID, "SKIPPED", "", nil)
		return nil
	}

	// 4) Re-read the bytes from storage (the upload's reader is long gone).
	rc, err := s.storage.Download(ctx, att.StoragePath)
	if err != nil {
		s.markTerminal(ctx, attachmentID, orgID, "FAILED", "", nil)
		return fmt.Errorf("download bytes: %w", err)
	}
	defer rc.Close()
	content, err := io.ReadAll(io.LimitReader(rc, s.maxBytes))
	if err != nil {
		s.markTerminal(ctx, attachmentID, orgID, "FAILED", "", nil)
		return fmt.Errorf("read bytes: %w", err)
	}

	// 5) Run extraction (the call to Document AI, behind the interface).
	res, err := s.extractor.Extract(ctx, content, att.ContentType, documentType)
	if err != nil {
		s.markTerminal(ctx, attachmentID, orgID, "FAILED", "", nil)
		return fmt.Errorf("extract: %w", err)
	}

	// 6) Persist the result and fill the expense — atomically (two tables).
	return s.saveResult(ctx, att, orgID, documentType, res)
}

// saveResult writes the OCR output to the attachment AND pre-fills the parent
// expense, in one transaction (per the project's "writes to >1 table → use a
// transaction" rule). The expense fill is COALESCE/CASE-guarded in SQL so it
// only ever populates EMPTY fields — it cannot clobber data a person entered.
func (s *OcrService) saveResult(ctx context.Context, att expenses.ExpenseAttachment, orgID uuid.UUID, documentType string, res *ExtractionResult) error {
	// Load the parent expense's currency for the money guard below. (A skeleton is
	// GBP, but reading it keeps us correct if that ever changes.)
	exp, err := s.queries.GetExpense(ctx, expenses.GetExpenseParams{
		ID:             att.ExpenseID,
		OrganisationID: orgID,
	})
	if err != nil {
		return fmt.Errorf("load expense for fill: %w", err)
	}

	// Money guard: only auto-fill the amount when the extracted currency matches
	// the expense's currency. Foreign-currency receipts need the FX handling the
	// manual flow does, so here we record the values in the JSON but pass 0 to the
	// money columns (FillExpenseFromOCR then leaves them at their placeholder).
	gross, vat := moneyToFill(exp.Currency, res)

	// Description: replace ONLY the skeleton's placeholder, never a description the
	// user has already typed. OCR runs on freshly-created skeletons (so the value
	// is the placeholder), but we guard regardless. No OCR description → keep the
	// current value. The SQL sets description = this value verbatim.
	description := exp.Description
	if res.Description != nil && exp.Description == placeholderDescription {
		description = *res.Description
	}

	// Build the "what OCR saw" JSON for ocr_extracted_data (badges, confidence
	// highlights, and an audit trail of which processor/type was used).
	extracted, err := json.Marshal(buildExtractedData(documentType, res))
	if err != nil {
		return fmt.Errorf("marshal extracted data: %w", err)
	}

	return s.withTx(ctx, func(qtx *expenses.Queries) error {
		if _, err := qtx.UpdateAttachmentOCRStatus(ctx, expenses.UpdateAttachmentOCRStatusParams{
			ID:               att.ID,
			OrganisationID:   orgID,
			OcrStatus:        "COMPLETE",
			OcrRawText:       textOrNull(res.RawText),
			OcrExtractedData: extracted,
		}); err != nil {
			return err
		}
		_, err := qtx.FillExpenseFromOCR(ctx, expenses.FillExpenseFromOCRParams{
			ID:                    att.ExpenseID,
			OrganisationID:        orgID,
			SupplierName:          pgNullText(res.SupplierName),
			SupplierVatNumber:     pgNullText(res.SupplierVAT),
			InvoiceNumber:         pgNullText(res.InvoiceNumber),
			Description:           description,
			DatedOn:               pgDateOrNull(res.Date),
			GrossValueMinor:       gross,
			NativeGrossValueMinor: gross, // GBP MVP: native == expense-currency value
			VatValueMinor:         vat,
			NativeVatValueMinor:   vat,
			OcrConfidence:         pgNumericFromDecimal(res.Confidence),
		})
		return err
	})
}

// markTerminal records a terminal OCR state (FAILED / SKIPPED, or COMPLETE when
// called directly) on the attachment. Best-effort: a failure to record is logged,
// not propagated, because we're already on the error/terminal path.
func (s *OcrService) markTerminal(ctx context.Context, attachmentID, orgID uuid.UUID, status, rawText string, extracted []byte) {
	if _, err := s.queries.UpdateAttachmentOCRStatus(ctx, expenses.UpdateAttachmentOCRStatusParams{
		ID:               attachmentID,
		OrganisationID:   orgID,
		OcrStatus:        status,
		OcrRawText:       textOrNull(rawText),
		OcrExtractedData: extracted,
	}); err != nil {
		log.Printf("ocr: could not mark attachment %s as %s: %v", attachmentID, status, err)
	}
}

// withTx runs fn inside a transaction, mirroring the wrappers in the expense and
// attachment services (each feature owns its own, kept deliberately small).
func (s *OcrService) withTx(ctx context.Context, fn func(*expenses.Queries) error) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}
	qtx := expenses.New(tx)
	if err := fn(qtx); err != nil {
		_ = tx.Rollback(ctx)
		return err
	}
	return tx.Commit(ctx)
}

// =============================================================================
// HELPERS
// =============================================================================

// moneyToFill decides what to write to the expense's money columns. It returns
// the gross and VAT in minor units, or 0/0 when the amount should NOT be applied
// (no amount found, or a currency mismatch). int64→int32 is clamped defensively;
// a real receipt never approaches the £21m int32 ceiling.
func moneyToFill(expenseCurrency string, res *ExtractionResult) (gross, vat int32) {
	if res.Currency != nil && !strings.EqualFold(*res.Currency, expenseCurrency) {
		return 0, 0 // foreign currency — leave the placeholder, value is in the JSON
	}
	if res.TotalMinor != nil {
		gross = clampToInt32(*res.TotalMinor)
	}
	if res.VATMinor != nil {
		vat = clampToInt32(*res.VATMinor)
	}
	return gross, vat
}

// extractedData is the JSON we store in expense_attachments.ocr_extracted_data —
// the "what OCR saw" record the frontend reads for badges and confidence hints.
// Amounts are pound strings (consistent with the rest of the API). document_type
// is recorded so a future re-run knows which processor produced this.
type extractedData struct {
	DocumentType  string  `json:"document_type"`
	Supplier      *string `json:"supplier,omitempty"`
	VATNumber     *string `json:"vat_number,omitempty"`
	InvoiceNumber *string `json:"invoice_number,omitempty"`
	Description   *string `json:"description,omitempty"` // assembled: supplier + line items
	Date          *string `json:"date,omitempty"`        // ISO 8601 (YYYY-MM-DD)
	Amount        *string `json:"amount,omitempty"`      // pounds, e.g. "42.00"
	VAT           *string `json:"vat,omitempty"`         // pounds
	Currency      *string `json:"currency,omitempty"`    // ISO 4217
	Confidence    string  `json:"confidence"`            // "0.93"
}

// buildExtractedData maps an ExtractionResult into the stored JSON shape.
func buildExtractedData(documentType string, res *ExtractionResult) extractedData {
	d := extractedData{
		DocumentType:  documentType,
		Supplier:      res.SupplierName,
		VATNumber:     res.SupplierVAT,
		InvoiceNumber: res.InvoiceNumber,
		Description:   res.Description,
		Currency:      res.Currency,
		Confidence:    res.Confidence.StringFixed(4),
	}
	if res.Date != nil {
		s := res.Date.Format("2006-01-02")
		d.Date = &s
	}
	if res.TotalMinor != nil {
		s := minorToPoundsInt64(*res.TotalMinor)
		d.Amount = &s
	}
	if res.VATMinor != nil {
		s := minorToPoundsInt64(*res.VATMinor)
		d.VAT = &s
	}
	return d
}

// buildExpenseDescription assembles a human expense description from the supplier
// and the document's line-item descriptions, per the agreed format:
//
//	supplier + 1 item   → "Supplier — Item"
//	supplier + 2+ items → "Supplier — N items"
//	supplier, no items  → "Supplier"
//	no supplier, items  → the top item (fallback)
//	nothing usable      → nil (so the skeleton's placeholder is kept)
//
// Em-dash separator; each item is trimmed and rune-capped for tidiness.
func buildExpenseDescription(supplier *string, items []string) *string {
	const sep = " — "
	const maxItemRunes = 80

	sup := ""
	if supplier != nil {
		sup = strings.TrimSpace(*supplier)
	}

	// Clean the line items: trim, drop blanks, cap length (by rune, not byte, so a
	// multi-byte character is never split).
	clean := make([]string, 0, len(items))
	for _, it := range items {
		it = strings.TrimSpace(it)
		if it == "" {
			continue
		}
		if r := []rune(it); len(r) > maxItemRunes {
			it = strings.TrimSpace(string(r[:maxItemRunes])) + "…"
		}
		clean = append(clean, it)
	}

	var out string
	switch {
	case sup != "" && len(clean) == 0:
		out = sup
	case sup != "" && len(clean) == 1:
		out = sup + sep + clean[0]
	case sup != "" && len(clean) >= 2:
		out = sup + sep + fmt.Sprintf("%d items", len(clean))
	case sup == "" && len(clean) >= 1:
		out = clean[0] // no supplier — fall back to the top item
	default:
		return nil // nothing usable → keep the placeholder
	}
	return &out
}

// minorToPoundsInt64 renders minor units (pence) as a 2dp string ("4200" → "42.00").
func minorToPoundsInt64(minor int64) string {
	return decimal.NewFromInt(minor).Div(decimal.NewFromInt(100)).StringFixed(2)
}

// clampToInt32 saturates an int64 into int32 range (money columns are INTEGER).
func clampToInt32(v int64) int32 {
	const maxI32, minI32 int64 = 1<<31 - 1, -(1 << 31)
	switch {
	case v > maxI32:
		return int32(maxI32)
	case v < minI32:
		return int32(minI32)
	default:
		return int32(v)
	}
}

// textOrNull maps a Go string to pgtype.Text, treating "" as SQL NULL.
func textOrNull(s string) pgtype.Text {
	if s == "" {
		return pgtype.Text{}
	}
	return pgtype.Text{String: s, Valid: true}
}

// pgDateOrNull maps an optional time to pgtype.Date; nil → SQL NULL.
func pgDateOrNull(t *time.Time) pgtype.Date {
	if t == nil {
		return pgtype.Date{}
	}
	return pgtype.Date{Time: *t, Valid: true}
}

// pgNumericFromDecimal maps a decimal to pgtype.Numeric (for ocr_confidence,
// NUMERIC(5,4)); an unparseable value becomes SQL NULL rather than erroring.
func pgNumericFromDecimal(d decimal.Decimal) pgtype.Numeric {
	var n pgtype.Numeric
	if err := n.Scan(d.StringFixed(4)); err != nil {
		return pgtype.Numeric{}
	}
	return n
}
