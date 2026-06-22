package main

// ocr_service_test.go
// =============================================================================
// Tests for the OCR / "Smart Upload" feature.
//
// Two layers:
//   - moneyToMinor is a PURE UNIT test (no infrastructure) — it pins the
//     MoneyValue→pence conversion, which is financial-correctness critical.
//   - The rest are integration tests: real PostgreSQL + the real GCS dev bucket
//     (like the attachment tests, they skip when GCS_BUCKET is unset). The
//     Document AI call itself is the ONE thing we fake — hitting the paid API in
//     every test run isn't practical, and external third-party services are the
//     sanctioned place for a mock (per the project's testing policy). We inject a
//     fakeExtractor and drive OcrService.process synchronously so the async
//     goroutine never makes the assertions racy.
// =============================================================================

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/joho/godotenv"
	"github.com/shopspring/decimal"

	auth "github.com/operationfb/accounting-saas/db/auth"
	dbexpenses "github.com/operationfb/accounting-saas/db/expenses"
	attachments "github.com/operationfb/accounting-saas/internal/attachments"
	expenses "github.com/operationfb/accounting-saas/internal/expenses"
	// Dot-import: these capture→OCR integration tests reference many of the ocr
	// package's symbols (OcrService, NewOcrService, ExtractionResult,
	// DocumentTypeReceipt, …). A dot-import in this test file keeps them unqualified
	// (as before the package split) without a wall of `ocr.` prefixes. Scoped to
	// this file only; production code always imports ocr qualified.
	. "github.com/operationfb/accounting-saas/internal/ocr"
)

// =============================================================================
// TEST DOUBLES
// =============================================================================

// fakeExtractor stands in for Document AI: it returns a canned result (or error)
// and records the document_type it was asked for, so a test can prove routing.
type fakeExtractor struct {
	result          *ExtractionResult
	err             error
	gotDocumentType string
}

func (f *fakeExtractor) Extract(_ context.Context, _ []byte, _ string, documentType string) (*ExtractionResult, error) {
	f.gotDocumentType = documentType
	if f.err != nil {
		return nil, f.err
	}
	return f.result, nil
}

// spyEnqueuer records Enqueue calls so a test can prove OCR was (or was NOT)
// triggered, without running the real background goroutine.
type spyEnqueuer struct {
	calls []spyEnqueueCall
}
type spyEnqueueCall struct {
	attID, orgID uuid.UUID
	docType      string
}

func (s *spyEnqueuer) Enqueue(attID, orgID uuid.UUID, documentType string) {
	s.calls = append(s.calls, spyEnqueueCall{attID, orgID, documentType})
}

// =============================================================================
// HELPERS
// =============================================================================

// captureAs runs a Smart Upload through a service wired with the given OCR
// enqueuer (usually a spy, so OCR doesn't auto-run), and registers cleanup of
// the expense + attachment + GCS object. Returns the new draft detail response.
func captureAs(t *testing.T, ts *testServer, ocr attachments.OcrEnqueuer, callerID, orgID, documentType, filename string, data []byte) *expenses.ExpenseDetailResponse {
	t.Helper()
	svc := attachments.NewService(ts.pool, dbexpenses.New(ts.pool), auth.New(ts.pool),
		ts.storage, ocr, 0, 0)
	resp, err := svc.CaptureFromReceipt(
		context.Background(), mustUUID(t, callerID), mustUUID(t, orgID),
		documentType, filename, int64(len(data)), bytes.NewReader(data),
	)
	if err != nil {
		t.Fatalf("CaptureFromReceipt(%s): %v", documentType, err)
	}
	t.Cleanup(func() {
		ctx := context.Background()
		if len(resp.Attachments) > 0 {
			// DeleteAttachment removes the metadata row AND the GCS object.
			_ = svc.DeleteAttachment(ctx, mustUUID(t, callerID), mustUUID(t, orgID), resp.ID, resp.Attachments[0].ID)
		}
		_, _ = ts.pool.Exec(ctx, "DELETE FROM expenses WHERE id = $1", resp.ID)
	})
	return resp
}

// newOCRService builds an OcrService against the test pool + real storage with
// the given (fake) extractor.
func newOCRService(ts *testServer, ext DocumentExtractor) *OcrService {
	return NewOcrService(ts.pool, dbexpenses.New(ts.pool), ts.storage, ext, attachments.PlaceholderDescription)
}

func containsExpense(list []*expenses.ExpenseResponse, id string) bool {
	for _, e := range list {
		if e.ID == id {
			return true
		}
	}
	return false
}

// =============================================================================
// SMART UPLOAD (capture)
// =============================================================================

// TestSmartUploadCapture covers the receipt-first capture path: a skeleton draft
// is created and OCR is enqueued; an invalid document_type is rejected; and the
// plain "Add file" path does NOT trigger OCR.
func TestSmartUploadCapture(t *testing.T) {
	requireGCS(t)
	ts := newTestServer(t)
	defer ts.pool.Close()

	t.Run("creates a needs_review skeleton and enqueues OCR", func(t *testing.T) {
		spy := &spyEnqueuer{}
		draft := captureAs(t, ts, spy, devUserID, devOrgID, DocumentTypeInvoice, "receipt.pdf", samplePDF())

		if !draft.NeedsReview {
			t.Error("a capture must have needs_review = true")
		}
		if draft.Status != "DRAFT" {
			t.Errorf("status: got %q, want DRAFT", draft.Status)
		}
		if draft.Description != "Awaiting review" {
			t.Errorf("description placeholder: got %q, want 'Awaiting review'", draft.Description)
		}
		if draft.GrossValue != "0.00" {
			t.Errorf("gross placeholder: got %q, want 0.00", draft.GrossValue)
		}
		if draft.CategoryNominalCode != attachments.PlaceholderCategoryNominal {
			t.Errorf("category: got %q, want the Sundries placeholder %q", draft.CategoryNominalCode, attachments.PlaceholderCategoryNominal)
		}

		if len(draft.Attachments) != 1 {
			t.Fatalf("expected exactly 1 attachment, got %d", len(draft.Attachments))
		}
		att := draft.Attachments[0]
		if !att.IsPrimary {
			t.Error("the capture's file must be primary")
		}
		if att.OCRStatus != "PENDING" {
			t.Errorf("ocr_status: got %q, want PENDING", att.OCRStatus)
		}

		if len(spy.calls) != 1 {
			t.Fatalf("expected exactly 1 Enqueue call, got %d", len(spy.calls))
		}
		call := spy.calls[0]
		if call.attID.String() != att.ID || call.orgID.String() != devOrgID || call.docType != DocumentTypeInvoice {
			t.Errorf("Enqueue args: got (att=%s org=%s type=%s), want (att=%s org=%s type=invoice)",
				call.attID, call.orgID, call.docType, att.ID, devOrgID)
		}
	})

	t.Run("invalid document_type is rejected", func(t *testing.T) {
		svc := attachments.NewService(ts.pool, dbexpenses.New(ts.pool), auth.New(ts.pool),
			ts.storage, &spyEnqueuer{}, 0, 0)
		_, err := svc.CaptureFromReceipt(context.Background(), mustUUID(t, devUserID), mustUUID(t, devOrgID),
			"banana", "x.pdf", int64(len(samplePDF())), bytes.NewReader(samplePDF()))
		assertAppCode(t, err, ErrCodeValidation)
	})

	t.Run("Add file does NOT enqueue OCR", func(t *testing.T) {
		// The standard attachment path must stay plain — no OCR, no needs_review.
		spy := &spyEnqueuer{}
		svc := attachments.NewService(ts.pool, dbexpenses.New(ts.pool), auth.New(ts.pool),
			ts.storage, spy, 0, 0)
		expenseID := createExpenseAs(t, ts, devUserID, devOrgID)
		resp, err := svc.UploadAttachment(context.Background(), mustUUID(t, devUserID), mustUUID(t, devOrgID),
			expenseID, "r.pdf", int64(len(samplePDF())), bytes.NewReader(samplePDF()), nil)
		if err != nil {
			t.Fatalf("UploadAttachment: %v", err)
		}
		t.Cleanup(func() {
			_ = svc.DeleteAttachment(context.Background(), mustUUID(t, devUserID), mustUUID(t, devOrgID), expenseID, resp.ID)
		})
		if len(spy.calls) != 0 {
			t.Errorf("Add file must NOT enqueue OCR, got %d Enqueue call(s)", len(spy.calls))
		}
		if resp.OCRStatus != "PENDING" {
			t.Errorf("ocr_status after Add file: got %q, want PENDING (no OCR run)", resp.OCRStatus)
		}
	})
}

// =============================================================================
// OCR PROCESSING (fill / no-overwrite / failure / skip / currency)
// =============================================================================

// TestOCRProcessFillsExpense drives OcrService.process synchronously with a fake
// extractor and asserts the result is written correctly.
func TestOCRProcessFillsExpense(t *testing.T) {
	requireGCS(t)
	ts := newTestServer(t)
	defer ts.pool.Close()

	devUser, devOrg := mustUUID(t, devUserID), mustUUID(t, devOrgID)

	t.Run("fills empty fields, sets confidence, keeps needs_review", func(t *testing.T) {
		draft := captureAs(t, ts, &spyEnqueuer{}, devUserID, devOrgID, DocumentTypeInvoice, "inv.pdf", samplePDF())
		attID := mustUUID(t, draft.Attachments[0].ID)

		supplier, vatno, invno := "Acme Ltd", "GB123456789", "INV-001"
		descr := "Acme Ltd — Office supplies"
		date := time.Date(2026, 6, 10, 0, 0, 0, 0, time.UTC)
		total, vat, cur := int64(4200), int64(700), "GBP"
		fake := &fakeExtractor{result: &ExtractionResult{
			RawText:       "ACME LTD ... TOTAL 42.00",
			SupplierName:  &supplier,
			SupplierVAT:   &vatno,
			InvoiceNumber: &invno,
			Description:   &descr,
			Currency:      &cur,
			Date:          &date,
			TotalMinor:    &total,
			VATMinor:      &vat,
			Confidence:    decimal.NewFromFloat(0.93),
		}}

		if err := newOCRService(ts, fake).Process(context.Background(), attID, devOrg, DocumentTypeInvoice); err != nil {
			t.Fatalf("process: %v", err)
		}
		if fake.gotDocumentType != DocumentTypeInvoice {
			t.Errorf("routing: extractor got document_type %q, want invoice", fake.gotDocumentType)
		}

		// Attachment → COMPLETE, with the extracted JSON carrying the VAT number.
		var ocrStatus string
		var extracted []byte
		if err := ts.pool.QueryRow(context.Background(),
			"SELECT ocr_status, ocr_extracted_data FROM expense_attachments WHERE id=$1", attID).Scan(&ocrStatus, &extracted); err != nil {
			t.Fatalf("read attachment: %v", err)
		}
		if ocrStatus != "COMPLETE" {
			t.Errorf("ocr_status: got %q, want COMPLETE", ocrStatus)
		}
		if !strings.Contains(string(extracted), "GB123456789") {
			t.Errorf("ocr_extracted_data should carry the VAT number, got: %s", extracted)
		}
		if !strings.Contains(string(extracted), descr) {
			t.Errorf("ocr_extracted_data should carry the description %q, got: %s", descr, extracted)
		}

		// Expense → fields filled; needs_review STILL true (OCR ≠ confirmation).
		detail, err := ts.expenseService.GetExpenseDetail(context.Background(), devUser, devOrg, draft.ID)
		if err != nil {
			t.Fatalf("GetExpenseDetail: %v", err)
		}
		if detail.SupplierName == nil || *detail.SupplierName != supplier {
			t.Errorf("supplier_name: got %v, want %q", detail.SupplierName, supplier)
		}
		if detail.SupplierVATNumber == nil || *detail.SupplierVATNumber != vatno {
			t.Errorf("supplier_vat_number: got %v, want %q", detail.SupplierVATNumber, vatno)
		}
		if detail.GrossValue != "42.00" {
			t.Errorf("gross_value: got %q, want 42.00", detail.GrossValue)
		}
		if detail.VATValue != "7.00" {
			t.Errorf("vat_value: got %q, want 7.00", detail.VATValue)
		}
		if detail.DatedOn != "2026-06-10" {
			t.Errorf("dated_on: got %q, want 2026-06-10", detail.DatedOn)
		}
		if detail.Description != descr {
			t.Errorf("description: got %q, want %q (assembled from OCR, replacing the placeholder)", detail.Description, descr)
		}
		if detail.OCRConfidence == nil {
			t.Error("ocr_confidence should be set after OCR")
		}
		if !detail.NeedsReview {
			t.Error("needs_review must STAY true after OCR (OCR is not human confirmation)")
		}
	})

	t.Run("keeps the placeholder description when OCR finds none", func(t *testing.T) {
		draft := captureAs(t, ts, &spyEnqueuer{}, devUserID, devOrgID, DocumentTypeReceipt, "r.pdf", samplePDF())
		attID := mustUUID(t, draft.Attachments[0].ID)

		x := "Some Supplier"
		fake := &fakeExtractor{result: &ExtractionResult{SupplierName: &x, Confidence: decimal.NewFromInt(1)}} // no Description
		if err := newOCRService(ts, fake).Process(context.Background(), attID, devOrg, DocumentTypeReceipt); err != nil {
			t.Fatalf("process: %v", err)
		}
		var desc string
		if err := ts.pool.QueryRow(context.Background(),
			"SELECT description FROM expenses WHERE id=$1", draft.ID).Scan(&desc); err != nil {
			t.Fatalf("read description: %v", err)
		}
		if desc != attachments.PlaceholderDescription {
			t.Errorf("description should remain the placeholder when OCR has none; got %q", desc)
		}
	})

	t.Run("does not overwrite a field the user already set", func(t *testing.T) {
		draft := captureAs(t, ts, &spyEnqueuer{}, devUserID, devOrgID, DocumentTypeInvoice, "inv.pdf", samplePDF())
		attID := mustUUID(t, draft.Attachments[0].ID)

		// Simulate the user typing a supplier before OCR finished.
		if _, err := ts.pool.Exec(context.Background(),
			"UPDATE expenses SET supplier_name='User Typed Ltd' WHERE id=$1", draft.ID); err != nil {
			t.Fatalf("preset supplier: %v", err)
		}

		ocrSupplier := "OCR Supplier Ltd"
		fake := &fakeExtractor{result: &ExtractionResult{SupplierName: &ocrSupplier, Confidence: decimal.NewFromInt(1)}}
		if err := newOCRService(ts, fake).Process(context.Background(), attID, devOrg, DocumentTypeInvoice); err != nil {
			t.Fatalf("process: %v", err)
		}

		var supplier string
		if err := ts.pool.QueryRow(context.Background(),
			"SELECT supplier_name FROM expenses WHERE id=$1", draft.ID).Scan(&supplier); err != nil {
			t.Fatalf("read supplier: %v", err)
		}
		if supplier != "User Typed Ltd" {
			t.Errorf("OCR must not overwrite a user-entered supplier; got %q", supplier)
		}
	})

	t.Run("extractor error marks FAILED and fills nothing", func(t *testing.T) {
		draft := captureAs(t, ts, &spyEnqueuer{}, devUserID, devOrgID, DocumentTypeReceipt, "r.jpg", sampleJPEG())
		attID := mustUUID(t, draft.Attachments[0].ID)

		fake := &fakeExtractor{err: errors.New("document AI exploded")}
		if err := newOCRService(ts, fake).Process(context.Background(), attID, devOrg, DocumentTypeReceipt); err == nil {
			t.Fatal("expected process to surface the extractor error")
		}

		var ocrStatus string
		var gross int32
		if err := ts.pool.QueryRow(context.Background(),
			`SELECT a.ocr_status, e.gross_value_minor
			   FROM expense_attachments a JOIN expenses e ON e.id = a.expense_id
			  WHERE a.id=$1`, attID).Scan(&ocrStatus, &gross); err != nil {
			t.Fatalf("read: %v", err)
		}
		if ocrStatus != "FAILED" {
			t.Errorf("ocr_status: got %q, want FAILED", ocrStatus)
		}
		if gross != 0 {
			t.Errorf("a failed OCR must not fill the expense; gross_value_minor=%d", gross)
		}
	})

	t.Run("oversized file is SKIPPED", func(t *testing.T) {
		draft := captureAs(t, ts, &spyEnqueuer{}, devUserID, devOrgID, DocumentTypeReceipt, "r.pdf", samplePDF())
		attID := mustUUID(t, draft.Attachments[0].ID)

		// maxBytes=4 (smaller than samplePDF()) so the size guard trips → SKIPPED.
		svc := NewOcrService(ts.pool, dbexpenses.New(ts.pool), ts.storage,
			&fakeExtractor{result: &ExtractionResult{}}, attachments.PlaceholderDescription, WithMaxBytes(4))
		if err := svc.Process(context.Background(), attID, devOrg, DocumentTypeReceipt); err != nil {
			t.Fatalf("process: %v", err)
		}
		var ocrStatus string
		if err := ts.pool.QueryRow(context.Background(),
			"SELECT ocr_status FROM expense_attachments WHERE id=$1", attID).Scan(&ocrStatus); err != nil {
			t.Fatalf("read: %v", err)
		}
		if ocrStatus != "SKIPPED" {
			t.Errorf("ocr_status: got %q, want SKIPPED", ocrStatus)
		}
	})

	t.Run("foreign/unsupported currency amount is still pre-filled for review", func(t *testing.T) {
		// A receipt in a currency that isn't the expense's (here RON, which we don't
		// support) must NOT be dropped to 0: we pre-fill the number in the expense's
		// currency units so the reviewer corrects it rather than re-keying it. The
		// capture stays needs_review=TRUE and the real currency is kept in the JSON.
		draft := captureAs(t, ts, &spyEnqueuer{}, devUserID, devOrgID, DocumentTypeInvoice, "inv.pdf", samplePDF())
		attID := mustUUID(t, draft.Attachments[0].ID)

		ron, total := "RON", int64(7298) // 72.98 RON
		fake := &fakeExtractor{result: &ExtractionResult{Currency: &ron, TotalMinor: &total, Confidence: decimal.NewFromInt(1)}}
		if err := newOCRService(ts, fake).Process(context.Background(), attID, devOrg, DocumentTypeInvoice); err != nil {
			t.Fatalf("process: %v", err)
		}
		var gross int32
		if err := ts.pool.QueryRow(context.Background(),
			"SELECT gross_value_minor FROM expenses WHERE id=$1", draft.ID).Scan(&gross); err != nil {
			t.Fatalf("read: %v", err)
		}
		if gross != 7298 {
			t.Errorf("foreign-currency amount should be pre-filled for review; gross_value_minor=%d, want 7298", gross)
		}
	})
}

// =============================================================================
// INBOX + CONFIRM + MULTI-TENANCY
// =============================================================================

// TestOCRInboxAndConfirm verifies the review inbox lists captures, is org-scoped,
// and that saving a capture (PUT) confirms it (clears needs_review, leaves inbox).
func TestOCRInboxAndConfirm(t *testing.T) {
	requireGCS(t)
	ts := newTestServer(t)
	defer ts.pool.Close()

	draft := captureAs(t, ts, &spyEnqueuer{}, devUserID, devOrgID, DocumentTypeReceipt, "r.pdf", samplePDF())

	// 1) The capture appears in this org's inbox.
	inbox, err := ts.expenseService.ListInbox(context.Background(), mustUUID(t, devUserID), mustUUID(t, devOrgID))
	if err != nil {
		t.Fatalf("ListInbox: %v", err)
	}
	if !containsExpense(inbox, draft.ID) {
		t.Errorf("the inbox should contain the capture %s", draft.ID)
	}

	// 2) Multi-tenant: a genuine member of another org never sees it.
	orgB, userB := newOrgWithOwner(t, ts)
	inboxB, err := ts.expenseService.ListInbox(context.Background(), mustUUID(t, userB), mustUUID(t, orgB))
	if err != nil {
		t.Fatalf("ListInbox orgB: %v", err)
	}
	if containsExpense(inboxB, draft.ID) {
		t.Error("another organisation must NOT see this org's capture in its inbox")
	}

	// 3) Confirm by saving the reviewed expense (PUT) → needs_review clears.
	rec := putExpense(t, ts, draft.ID, bearer(t, ts, devUserID, devOrgID), validUpdateBody(t, ts))
	if rec.Code != http.StatusOK {
		t.Fatalf("confirm update: expected 200, got %d — body: %s", rec.Code, rec.Body.String())
	}
	if got := decodeExpense(t, rec); got.NeedsReview {
		t.Error("saving a reviewed capture must clear needs_review")
	}

	// 4) ...and it has left the inbox.
	inboxAfter, err := ts.expenseService.ListInbox(context.Background(), mustUUID(t, devUserID), mustUUID(t, devOrgID))
	if err != nil {
		t.Fatalf("ListInbox after confirm: %v", err)
	}
	if containsExpense(inboxAfter, draft.ID) {
		t.Error("a confirmed capture must leave the review inbox")
	}
}

// =============================================================================
// LIVE DOCUMENT AI (opt-in — makes real, billed API calls)
// =============================================================================
//
// TestDocumentAILive runs a real receipt/invoice through the REAL Document AI
// (both processors), through the genuine capture → OCR pipeline. It is the only
// test that hits the paid API, so it is doubly gated: it skips unless GCS is
// configured AND the DOCAI_* vars are set AND DOCAI_LIVE_TEST is set. That keeps
// routine `go test ./...` fast and free — even with DOCAI_* configured for the
// running app — and makes real billing strictly opt-in:
//
//	DOCAI_LIVE_TEST=1 go test -run TestDocumentAILive -v
//
// It asserts the pipeline reaches COMPLETE and that real text was extracted, and
// logs the fields Document AI returned so you can eyeball the extraction.
func TestDocumentAILive(t *testing.T) {
	requireGCS(t)
	requireDocAILive(t)
	ts := newTestServer(t)
	defer ts.pool.Close()

	// Build the REAL extractor from the .env config — this is the connection
	// (auth + API + processor ids + region) under test.
	ext, err := NewDocumentAIExtractor(context.Background(),
		os.Getenv("DOCAI_PROJECT_ID"),
		envOr("DOCAI_LOCATION", "eu"),
		os.Getenv("DOCAI_INVOICE_PROCESSOR_ID"),
		os.Getenv("DOCAI_EXPENSE_PROCESSOR_ID"))
	if err != nil {
		t.Fatalf("could not build Document AI extractor: %v", err)
	}
	ocrSvc := NewOcrService(ts.pool, dbexpenses.New(ts.pool), ts.storage, ext, attachments.PlaceholderDescription)
	devOrg := mustUUID(t, devOrgID)
	pdf := buildInvoicePDF() // a valid single-page PDF with real invoice text

	for _, c := range []struct{ name, docType string }{
		{"Invoice Parser", DocumentTypeInvoice},
		{"Expense Parser", DocumentTypeReceipt},
	} {
		t.Run(c.name, func(t *testing.T) {
			draft := captureAs(t, ts, &spyEnqueuer{}, devUserID, devOrgID, c.docType, "live-"+c.docType+".pdf", pdf)
			attID := mustUUID(t, draft.Attachments[0].ID)

			// Drive the real pipeline synchronously (process is what the background
			// goroutine calls) so the assertions aren't racy.
			if err := ocrSvc.Process(context.Background(), attID, devOrg, c.docType); err != nil {
				t.Fatalf("REAL Document AI process(%s) failed — check the API is enabled, the SA has 'Document AI API User', and the processor id/region are correct: %v", c.docType, err)
			}

			var status string
			var rawText *string
			var extracted []byte
			if err := ts.pool.QueryRow(context.Background(),
				"SELECT ocr_status, ocr_raw_text, ocr_extracted_data FROM expense_attachments WHERE id=$1", attID).
				Scan(&status, &rawText, &extracted); err != nil {
				t.Fatalf("read attachment: %v", err)
			}
			if status != "COMPLETE" {
				t.Fatalf("ocr_status: got %q, want COMPLETE", status)
			}
			if rawText == nil || strings.TrimSpace(*rawText) == "" {
				t.Errorf("expected Document AI to return some extracted text, got none")
			}

			// Surface what the real API actually produced (visible with -v).
			detail, err := ts.expenseService.GetExpenseDetail(context.Background(), mustUUID(t, devUserID), devOrg, draft.ID)
			if err != nil {
				t.Fatalf("GetExpenseDetail: %v", err)
			}
			t.Logf("[%s] COMPLETE — raw_text=%d chars; extracted_data=%s", c.docType, len(*rawText), string(extracted))
			t.Logf("[%s] expense filled → supplier=%q vat_no=%q invoice_no=%q description=%q dated_on=%s gross=%s vat=%s confidence=%q",
				c.docType, deref(detail.SupplierName), deref(detail.SupplierVATNumber), deref(detail.InvoiceNumber),
				detail.Description, detail.DatedOn, detail.GrossValue, detail.VATValue, deref(detail.OCRConfidence))
		})
	}
}

// requireDocAILive skips unless the live Document AI test is explicitly opted in
// (DOCAI_LIVE_TEST set) and the processor config is present.
func requireDocAILive(t *testing.T) {
	t.Helper()
	_ = godotenv.Load()
	if os.Getenv("DOCAI_LIVE_TEST") == "" {
		t.Skip("DOCAI_LIVE_TEST not set — skipping the live (billed) Document AI test")
	}
	if os.Getenv("DOCAI_PROJECT_ID") == "" ||
		os.Getenv("DOCAI_INVOICE_PROCESSOR_ID") == "" ||
		os.Getenv("DOCAI_EXPENSE_PROCESSOR_ID") == "" {
		t.Skip("DOCAI_PROJECT_ID / DOCAI_*_PROCESSOR_ID not set — skipping live Document AI test")
	}
}

// deref renders an optional string for logging.
func deref(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

// buildInvoicePDF assembles a minimal but VALID single-page PDF (correct xref
// offsets, Helvetica base font) containing real, invoice-shaped text — enough for
// Document AI to read. The fake sample blobs elsewhere are content-free stubs the
// real API would reject, hence this generator.
func buildInvoicePDF() []byte {
	lines := []string{
		"ACME SUPPLIES LTD",
		"123 High Street, London, EC1A 1BB",
		"VAT Reg No: GB123456789",
		"",
		"INVOICE",
		"Invoice Number: INV-2026-001",
		"Invoice Date: 10/06/2026",
		"",
		"Description              Amount",
		"Office supplies           35.00",
		"",
		"Subtotal:                 35.00",
		"VAT (20%):                 7.00",
		"Total:                    42.00 GBP",
	}

	// Page content stream: a text object positioning each line down the page.
	var content bytes.Buffer
	content.WriteString("BT\n/F1 12 Tf\n")
	y := 760
	for _, ln := range lines {
		fmt.Fprintf(&content, "1 0 0 1 72 %d Tm\n(%s) Tj\n", y, pdfEscape(ln))
		y -= 20
	}
	content.WriteString("ET\n")
	stream := content.Bytes()

	objs := []string{
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
		"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Resources << /Font << /F1 4 0 R >> >> /Contents 5 0 R >>",
		"<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>",
		fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", len(stream), stream),
	}

	var buf bytes.Buffer
	buf.WriteString("%PDF-1.4\n")
	offsets := make([]int, len(objs)+1)
	for i, body := range objs {
		offsets[i+1] = buf.Len()
		fmt.Fprintf(&buf, "%d 0 obj\n%s\nendobj\n", i+1, body)
	}
	xref := buf.Len()
	fmt.Fprintf(&buf, "xref\n0 %d\n0000000000 65535 f \n", len(objs)+1)
	for i := 1; i <= len(objs); i++ {
		fmt.Fprintf(&buf, "%010d 00000 n \n", offsets[i])
	}
	fmt.Fprintf(&buf, "trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", len(objs)+1, xref)
	return buf.Bytes()
}

// pdfEscape escapes the characters that are special inside a PDF literal string.
func pdfEscape(s string) string {
	return strings.NewReplacer("\\", "\\\\", "(", "\\(", ")", "\\)").Replace(s)
}
