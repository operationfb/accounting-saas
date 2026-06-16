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
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
	money "google.golang.org/genproto/googleapis/type/money"

	auth "github.com/operationfb/accounting-saas/db/auth"
	expenses "github.com/operationfb/accounting-saas/db/expenses"
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
func captureAs(t *testing.T, ts *testServer, ocr ocrEnqueuer, callerID, orgID, documentType, filename string, data []byte) *ExpenseDetailResponse {
	t.Helper()
	svc := NewAttachmentService(ts.pool, expenses.New(ts.pool), auth.New(ts.pool),
		ts.server.attachmentService.storage, ocr, 0, 0)
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
	return NewOcrService(ts.pool, expenses.New(ts.pool), ts.server.attachmentService.storage, ext)
}

func containsExpense(list []*ExpenseResponse, id string) bool {
	for _, e := range list {
		if e.ID == id {
			return true
		}
	}
	return false
}

// =============================================================================
// MONEY CONVERSION (pure unit — always runs)
// =============================================================================

// TestOCRMoneyToMinor pins the Document AI MoneyValue → integer pence conversion,
// including half-up rounding on a half-penny. No float drift is permitted.
func TestOCRMoneyToMinor(t *testing.T) {
	cases := []struct {
		name      string
		units     int64
		nanos     int32
		wantMinor int64
	}{
		{"whole pounds", 42, 0, 4200},
		{"pence", 42, 990_000_000, 4299},              // £42.99
		{"five pence", 0, 50_000_000, 5},              // £0.05
		{"half penny rounds up", 42, 5_000_000, 4201}, // £42.005 → 4200.5p → 4201
		{"zero", 0, 0, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			m := &money.Money{CurrencyCode: "GBP", Units: c.units, Nanos: c.nanos}
			if got := moneyToMinor(m); got != c.wantMinor {
				t.Errorf("moneyToMinor(units=%d nanos=%d) = %d, want %d", c.units, c.nanos, got, c.wantMinor)
			}
		})
	}
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
		if draft.CategoryNominalCode != placeholderCategoryNominal {
			t.Errorf("category: got %q, want the Sundries placeholder %q", draft.CategoryNominalCode, placeholderCategoryNominal)
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
		svc := NewAttachmentService(ts.pool, expenses.New(ts.pool), auth.New(ts.pool),
			ts.server.attachmentService.storage, &spyEnqueuer{}, 0, 0)
		_, err := svc.CaptureFromReceipt(context.Background(), mustUUID(t, devUserID), mustUUID(t, devOrgID),
			"banana", "x.pdf", int64(len(samplePDF())), bytes.NewReader(samplePDF()))
		assertAppCode(t, err, ErrCodeValidation)
	})

	t.Run("Add file does NOT enqueue OCR", func(t *testing.T) {
		// The standard attachment path must stay plain — no OCR, no needs_review.
		spy := &spyEnqueuer{}
		svc := NewAttachmentService(ts.pool, expenses.New(ts.pool), auth.New(ts.pool),
			ts.server.attachmentService.storage, spy, 0, 0)
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
		date := time.Date(2026, 6, 10, 0, 0, 0, 0, time.UTC)
		total, vat, cur := int64(4200), int64(700), "GBP"
		fake := &fakeExtractor{result: &ExtractionResult{
			RawText:       "ACME LTD ... TOTAL 42.00",
			SupplierName:  &supplier,
			SupplierVAT:   &vatno,
			InvoiceNumber: &invno,
			Currency:      &cur,
			Date:          &date,
			TotalMinor:    &total,
			VATMinor:      &vat,
			Confidence:    decimal.NewFromFloat(0.93),
		}}

		if err := newOCRService(ts, fake).process(context.Background(), attID, devOrg, DocumentTypeInvoice); err != nil {
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

		// Expense → fields filled; needs_review STILL true (OCR ≠ confirmation).
		detail, err := ts.server.expenseService.GetExpenseDetail(context.Background(), devUser, devOrg, draft.ID)
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
		if detail.OCRConfidence == nil {
			t.Error("ocr_confidence should be set after OCR")
		}
		if !detail.NeedsReview {
			t.Error("needs_review must STAY true after OCR (OCR is not human confirmation)")
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
		if err := newOCRService(ts, fake).process(context.Background(), attID, devOrg, DocumentTypeInvoice); err != nil {
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
		if err := newOCRService(ts, fake).process(context.Background(), attID, devOrg, DocumentTypeReceipt); err == nil {
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

		svc := newOCRService(ts, &fakeExtractor{result: &ExtractionResult{}})
		svc.maxBytes = 4 // smaller than samplePDF(), so the guard trips
		if err := svc.process(context.Background(), attID, devOrg, DocumentTypeReceipt); err != nil {
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

	t.Run("foreign currency is not auto-filled into money", func(t *testing.T) {
		draft := captureAs(t, ts, &spyEnqueuer{}, devUserID, devOrgID, DocumentTypeInvoice, "inv.pdf", samplePDF())
		attID := mustUUID(t, draft.Attachments[0].ID)

		usd, total := "USD", int64(5000)
		fake := &fakeExtractor{result: &ExtractionResult{Currency: &usd, TotalMinor: &total, Confidence: decimal.NewFromInt(1)}}
		if err := newOCRService(ts, fake).process(context.Background(), attID, devOrg, DocumentTypeInvoice); err != nil {
			t.Fatalf("process: %v", err)
		}
		var gross int32
		if err := ts.pool.QueryRow(context.Background(),
			"SELECT gross_value_minor FROM expenses WHERE id=$1", draft.ID).Scan(&gross); err != nil {
			t.Fatalf("read: %v", err)
		}
		if gross != 0 {
			t.Errorf("a USD amount must not fill a GBP expense; gross_value_minor=%d", gross)
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
	inbox, err := ts.server.expenseService.ListInbox(context.Background(), mustUUID(t, devUserID), mustUUID(t, devOrgID))
	if err != nil {
		t.Fatalf("ListInbox: %v", err)
	}
	if !containsExpense(inbox, draft.ID) {
		t.Errorf("the inbox should contain the capture %s", draft.ID)
	}

	// 2) Multi-tenant: a genuine member of another org never sees it.
	orgB, userB := newOrgWithOwner(t, ts)
	inboxB, err := ts.server.expenseService.ListInbox(context.Background(), mustUUID(t, userB), mustUUID(t, orgB))
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
	inboxAfter, err := ts.server.expenseService.ListInbox(context.Background(), mustUUID(t, devUserID), mustUUID(t, devOrgID))
	if err != nil {
		t.Fatalf("ListInbox after confirm: %v", err)
	}
	if containsExpense(inboxAfter, draft.ID) {
		t.Error("a confirmed capture must leave the review inbox")
	}
}
