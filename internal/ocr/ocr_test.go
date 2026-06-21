package ocr

// ocr_test.go
// =============================================================================
// PURE UNIT tests for the OCR package — no infrastructure (no Postgres, no GCS,
// no Document AI). They pin the financial-correctness-critical money conversion
// and the description-assembly helpers, so they live in-package and exercise the
// unexported functions directly. The capture→OCR INTEGRATION tests (real
// Postgres + GCS, fake extractor) stay in the root package, where they can reach
// AttachmentService.CaptureFromReceipt and the expense DTOs they assert on.
// =============================================================================

import (
	"testing"

	"cloud.google.com/go/documentai/apiv1/documentaipb"
	money "google.golang.org/genproto/googleapis/type/money"
)

// TestMoneyToMinor pins the Document AI MoneyValue → integer pence conversion,
// including half-up rounding on a half-penny. No float drift is permitted.
func TestMoneyToMinor(t *testing.T) {
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

// TestBuildExpenseDescription pins the supplier + line-item → description format.
func TestBuildExpenseDescription(t *testing.T) {
	sp := func(s string) *string { return &s }
	cases := []struct {
		name     string
		supplier *string
		items    []string
		want     *string
	}{
		{"supplier, no items", sp("Pret A Manger"), nil, sp("Pret A Manger")},
		{"supplier, one item", sp("Pret A Manger"), []string{"Flat White"}, sp("Pret A Manger — Flat White")},
		{"supplier, three items", sp("Pret A Manger"), []string{"Flat White", "Croissant", "Juice"}, sp("Pret A Manger — 3 items")},
		{"supplier, blank items ignored", sp("Pret A Manger"), []string{"  ", ""}, sp("Pret A Manger")},
		{"no supplier, items → top item", nil, []string{"Flat White", "Croissant"}, sp("Flat White")},
		{"nothing → nil", nil, nil, nil},
		{"blank supplier, no items → nil", sp("   "), nil, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := buildExpenseDescription(c.supplier, c.items)
			switch {
			case c.want == nil && got != nil:
				t.Errorf("got %q, want nil", *got)
			case c.want != nil && got == nil:
				t.Errorf("got nil, want %q", *c.want)
			case c.want != nil && got != nil && *got != *c.want:
				t.Errorf("got %q, want %q", *got, *c.want)
			}
		})
	}
}

// TestMapDocumentLineItemsToDescription proves the line items the parser returns
// (nested line_item/description) — previously discarded — now drive the assembled
// description, and validates the nested entity type name.
func TestMapDocumentLineItemsToDescription(t *testing.T) {
	lineItem := func(desc string) *documentaipb.Document_Entity {
		return &documentaipb.Document_Entity{
			Type: "line_item",
			Properties: []*documentaipb.Document_Entity{
				{Type: "line_item/description", MentionText: desc},
			},
		}
	}

	t.Run("supplier + 2 line items → count", func(t *testing.T) {
		res := mapDocumentToResult(&documentaipb.Document{
			Entities: []*documentaipb.Document_Entity{
				{Type: "supplier_name", MentionText: "ACME Cafe"},
				lineItem("Flat White"),
				lineItem("Croissant"),
			},
		})
		if res.Description == nil || *res.Description != "ACME Cafe — 2 items" {
			t.Errorf("description: got %v, want \"ACME Cafe — 2 items\"", res.Description)
		}
	})

	t.Run("supplier + 1 line item → item text", func(t *testing.T) {
		res := mapDocumentToResult(&documentaipb.Document{
			Entities: []*documentaipb.Document_Entity{
				{Type: "supplier_name", MentionText: "ACME Cafe"},
				lineItem("Large Flat White"),
			},
		})
		if res.Description == nil || *res.Description != "ACME Cafe — Large Flat White" {
			t.Errorf("description: got %v, want \"ACME Cafe — Large Flat White\"", res.Description)
		}
	})
}
