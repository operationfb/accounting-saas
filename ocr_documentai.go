package main

// ocr_documentai.go
// =============================================================================
// documentAIExtractor — the Google Document AI implementation of
// DocumentExtractor (ocr_service.go). It is to Document AI what gcsStorage is to
// GCS: the one concrete adapter behind an interface, reached via Application
// Default Credentials (no key files in the repo).
//
// Two processors, routed by the Smart Upload toggle:
//   - receipt → the Expense Parser  (photographed POS/till receipts)
//   - invoice → the Invoice Parser  (PDF invoices/bills; ~46 fields incl. the
//     supplier VAT number, which matters for UK VAT reclaim)
//
// DATA RESIDENCY (a hard requirement): we MUST pin the regional endpoint. The
// default global endpoint routes to the US; for the EU multi-region the host is
// `eu-documentai.googleapis.com` and processor names embed `/locations/eu/`.
// =============================================================================

import (
	"context"
	"fmt"
	"strings"
	"time"

	documentai "cloud.google.com/go/documentai/apiv1"
	"cloud.google.com/go/documentai/apiv1/documentaipb"
	"github.com/shopspring/decimal"
	"google.golang.org/api/option"
	money "google.golang.org/genproto/googleapis/type/money"
)

// documentAIExtractor holds the shared client plus the two fully-qualified
// processor resource names (projects/<p>/locations/<loc>/processors/<id>).
type documentAIExtractor struct {
	client           *documentai.DocumentProcessorClient
	invoiceProcessor string
	receiptProcessor string
}

// newDocumentAIExtractor builds the extractor against a regional endpoint.
// location is the Document AI multi-region (use "eu" for UK/EU data residency);
// the two processor IDs are the bare ids from the Cloud console.
func newDocumentAIExtractor(ctx context.Context, projectID, location, invoiceProcessorID, receiptProcessorID string) (*documentAIExtractor, error) {
	// Regional endpoint — REQUIRED for residency. e.g. "eu-documentai.googleapis.com:443".
	endpoint := fmt.Sprintf("%s-documentai.googleapis.com:443", location)
	client, err := documentai.NewDocumentProcessorClient(ctx, option.WithEndpoint(endpoint))
	if err != nil {
		return nil, fmt.Errorf("create Document AI client: %w", err)
	}
	name := func(id string) string {
		return fmt.Sprintf("projects/%s/locations/%s/processors/%s", projectID, location, id)
	}
	return &documentAIExtractor{
		client:           client,
		invoiceProcessor: name(invoiceProcessorID),
		receiptProcessor: name(receiptProcessorID),
	}, nil
}

// Extract sends the bytes to the processor matching documentType and maps the
// response into the engine-agnostic ExtractionResult.
func (e *documentAIExtractor) Extract(ctx context.Context, content []byte, contentType, documentType string) (*ExtractionResult, error) {
	processor := e.receiptProcessor
	if documentType == DocumentTypeInvoice {
		processor = e.invoiceProcessor
	}

	resp, err := e.client.ProcessDocument(ctx, &documentaipb.ProcessRequest{
		Name: processor,
		Source: &documentaipb.ProcessRequest_RawDocument{
			RawDocument: &documentaipb.RawDocument{
				Content:  content,
				MimeType: contentType,
			},
		},
	})
	if err != nil {
		return nil, fmt.Errorf("process document: %w", err)
	}
	return mapDocumentToResult(resp.GetDocument()), nil
}

// mapDocumentToResult walks the document's entities and pulls out the fields we
// care about. The Invoice and Expense parsers share many entity type names
// (supplier_name, total_amount, total_tax_amount); where they differ we accept
// both spellings (e.g. invoice_id vs receipt_date). Unknown entities are ignored.
func mapDocumentToResult(doc *documentaipb.Document) *ExtractionResult {
	res := &ExtractionResult{RawText: doc.GetText()}

	var confSum float64
	var confCount int

	for _, ent := range doc.GetEntities() {
		confSum += float64(ent.GetConfidence())
		confCount++

		switch ent.GetType() {
		case "supplier_name":
			res.SupplierName = textPtr(ent.GetMentionText())

		case "supplier_tax_id", "vat", "supplier_vat_number":
			// The Invoice Parser exposes the supplier's tax/VAT id; the Expense
			// Parser has no such field, so this is typically nil for receipts.
			res.SupplierVAT = textPtr(ent.GetMentionText())

		case "invoice_id", "invoice_number", "receipt_id":
			res.InvoiceNumber = textPtr(ent.GetMentionText())

		case "total_amount":
			// The grand total is VAT-INCLUSIVE — exactly what we store as
			// gross_value_minor. The MoneyValue also carries the ISO currency.
			if m := ent.GetNormalizedValue().GetMoneyValue(); m != nil {
				minor := moneyToMinor(m)
				res.TotalMinor = &minor
				if code := m.GetCurrencyCode(); code != "" {
					res.Currency = textPtr(code)
				}
			}

		case "total_tax_amount":
			if m := ent.GetNormalizedValue().GetMoneyValue(); m != nil {
				minor := moneyToMinor(m)
				res.VATMinor = &minor
			}

		case "invoice_date", "receipt_date", "purchase_date", "purchase_time":
			if d := ent.GetNormalizedValue().GetDateValue(); d != nil {
				// date.Date is a plain Y/M/D with no timezone; treat as a calendar
				// date at UTC midnight (we only ever read the Y/M/D back out).
				t := time.Date(int(d.GetYear()), time.Month(d.GetMonth()), int(d.GetDay()), 0, 0, 0, 0, time.UTC)
				if t.Year() > 1 { // ignore an all-zero / unparseable date
					res.Date = &t
				}
			}
		}
	}

	if confCount > 0 {
		// Mean entity confidence, rounded to the 4dp the ocr_confidence column holds.
		res.Confidence = decimal.NewFromFloat(confSum / float64(confCount)).Round(4)
	}
	return res
}

// moneyToMinor converts a Document AI MoneyValue to integer minor units (pence)
// of its currency, rounding HALF-UP. MoneyValue is { Units int64, Nanos int32 }
// where the amount = Units + Nanos/1e9 (Nanos are billionths). Minor units are
// hundredths, so:  minor = round( (Units + Nanos/1e9) * 100 ).
//
// We use shopspring/decimal throughout — never float — so the rounding is exact
// and deterministic. Worked examples (the OCR money unit test pins these):
//
//	£42.00  → Units 42, Nanos 0           → 4200
//	£42.99  → Units 42, Nanos 990000000   → 4299
//	£0.05   → Units 0,  Nanos 50000000    → 5
//	£42.005 → Units 42, Nanos 5000000     → 4201  (4200.5p, half-up)
func moneyToMinor(m *money.Money) int64 {
	units := decimal.NewFromInt(m.GetUnits())
	nanos := decimal.NewFromInt(int64(m.GetNanos())).Div(decimal.NewFromInt(1_000_000_000))
	// Round(0) on shopspring rounds half away from zero, which is HALF-UP for the
	// non-negative amounts we expect on a receipt.
	return units.Add(nanos).Mul(decimal.NewFromInt(100)).Round(0).IntPart()
}

// textPtr trims a mention string and returns nil for an empty/whitespace value,
// so "not found" cleanly becomes a nil *string in ExtractionResult.
func textPtr(s string) *string {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	return &s
}
