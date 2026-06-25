package vat

// calculate_test.go
// =============================================================================
// Pure unit tests for the VAT box engine — no DB, no HTTP. They exercise
// `routeToBoxes` (the box maths) directly across the vat_status / ec_status /
// direction matrix, and `computeReturn` end-to-end with hand-built rows (incl. the
// reference screenshot's worked example and the per-source sign transforms).
// =============================================================================

import (
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	vatdb "github.com/operationfb/accounting-saas/db/vat"
)

func pgD(s string) pgtype.Date {
	t, err := time.Parse("2006-01-02", s)
	if err != nil {
		panic(err)
	}
	return pgtype.Date{Time: t, Valid: true}
}
func pgT(s string) pgtype.Text { return pgtype.Text{String: s, Valid: true} }

// =============================================================================
// routeToBoxes — the box-routing matrix
// =============================================================================

func TestRouteToBoxes(t *testing.T) {
	const vat, net = int64(2000), int64(10000)
	cases := []struct {
		name      string
		direction string
		vatStatus string
		ecStatus  string
		want      vatBoxes // only the boxes we set; Box3/Box5 are not derived here
	}{
		{"uk output", dirOutput, vatStatusTaxable, ecUKNonEC, vatBoxes{Box1: vat, Box6: net}},
		{"uk input", dirInput, vatStatusTaxable, ecUKNonEC, vatBoxes{Box4: vat, Box7: net}},
		{"exempt input → net only", dirInput, vatStatusExempt, ecUKNonEC, vatBoxes{Box7: net}},
		{"exempt output → net only", dirOutput, vatStatusExempt, ecUKNonEC, vatBoxes{Box6: net}},
		{"out of scope → nothing", dirInput, vatStatusOutOfScope, ecUKNonEC, vatBoxes{}},
		// Reverse charge on a purchase: notional VAT to BOTH 1 and 4, net to BOTH 6 and 7.
		{"reverse charge input", dirInput, vatStatusTaxable, ecReverseCharge, vatBoxes{Box1: vat, Box4: vat, Box6: net, Box7: net}},
		{"ec services input (reverse charge)", dirInput, vatStatusTaxable, ecServices, vatBoxes{Box1: vat, Box4: vat, Box6: net, Box7: net}},
		// EC goods acquisition: VAT to 2 and 4, net to 9 and 7.
		{"ec goods acquisition (input)", dirInput, vatStatusTaxable, ecGoods, vatBoxes{Box2: vat, Box4: vat, Box7: net, Box9: net}},
		// EC goods dispatch (a sale): net to 6 and 8, zero-rated.
		{"ec goods dispatch (output)", dirOutput, vatStatusTaxable, ecGoods, vatBoxes{Box6: net, Box8: net}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var b vatBoxes
			routeToBoxes(c.direction, c.vatStatus, c.ecStatus, vat, net, &b)
			if b != c.want {
				t.Errorf("routeToBoxes(%s/%s/%s) = %+v, want %+v", c.direction, c.vatStatus, c.ecStatus, b, c.want)
			}
		})
	}
}

// =============================================================================
// computeReturn — end-to-end with hand-built rows
// =============================================================================

// The reference screenshot: a single £1,987 (incl. 20% VAT) expense. Stored POSITIVE
// (gross 198700, VAT 33117 extracted) → input purchase: Box4 £331.17, Box7 net
// £1,655.83 (rounded to £1,656). No sales, so Box1/Box6 = 0 and Box5 = −£331.17 (a
// reclaim).
func TestComputeReturn_ScreenshotExpense(t *testing.T) {
	expenses := []vatdb.ListExpensesForVatReturnRow{{
		ID:                    [16]byte{1},
		DatedOn:               pgD("2026-04-07"),
		Description:           "Kamado Joe grill",
		CategoryName:          "Sundries",
		NativeGrossValueMinor: 198700,
		NativeVatValueMinor:   33117,
		VatStatus:             vatStatusTaxable,
		EcStatus:              ecUKNonEC,
	}}

	b, sales, purchases := computeReturnAccrual(expenses, nil, nil, nil)

	if b.Box4 != 33117 || b.Box7 != 165583 {
		t.Errorf("Box4=%d Box7=%d, want 33117 and 165583", b.Box4, b.Box7)
	}
	if b.Box1 != 0 || b.Box6 != 0 || b.Box3 != 0 {
		t.Errorf("output boxes should be zero, got Box1=%d Box6=%d Box3=%d", b.Box1, b.Box6, b.Box3)
	}
	if b.Box5 != -33117 {
		t.Errorf("Box5=%d, want -33117 (a reclaim)", b.Box5)
	}
	if len(sales) != 0 || len(purchases) != 1 {
		t.Fatalf("lines: got %d sales / %d purchases, want 0 / 1", len(sales), len(purchases))
	}

	var resp VatReturnResponse
	boxesToResponse(b, &resp)
	if resp.Box4 != "331.17" {
		t.Errorf("Box4 string = %q, want %q", resp.Box4, "331.17")
	}
	if resp.Box7 != "1656.00" { // net rounded to the whole pound
		t.Errorf("Box7 string = %q, want %q", resp.Box7, "1656.00")
	}
	if resp.Box5 != "-331.17" || resp.NetDue != "-331.17" || !resp.IsReclaim {
		t.Errorf("Box5=%q NetDue=%q IsReclaim=%v, want -331.17/-331.17/true", resp.Box5, resp.NetDue, resp.IsReclaim)
	}
	// The Full-Report line keeps the exact net (not the rounded box total).
	pl := linesToResponse(purchases)
	if pl[0].Net != "1655.83" || pl[0].Vat != "331.17" {
		t.Errorf("purchase line net=%q vat=%q, want 1655.83 / 331.17", pl[0].Net, pl[0].Vat)
	}
}

// An invoice (output) + the box derivations: Box1/Box6 from the invoice, Box3=Box1,
// Box5=Box3-Box4 positive (payable).
func TestComputeReturn_InvoiceSale(t *testing.T) {
	invoices := []vatdb.ListInvoicesForVatReturnRow{{
		ID: [16]byte{2}, DatedOn: pgD("2026-04-10"), Reference: pgT("001"),
		NetValueMinor: 100000, SalesTaxValueMinor: 20000,
	}}
	b, sales, purchases := computeReturnAccrual(nil, invoices, nil, nil)
	if b.Box1 != 20000 || b.Box6 != 100000 || b.Box3 != 20000 || b.Box5 != 20000 {
		t.Errorf("got Box1=%d Box6=%d Box3=%d Box5=%d, want 20000/100000/20000/20000", b.Box1, b.Box6, b.Box3, b.Box5)
	}
	if len(sales) != 1 || len(purchases) != 0 {
		t.Errorf("lines: got %d sales / %d purchases, want 1 / 0", len(sales), len(purchases))
	}
}

// A bill (positive, input) contributes to Box4/Box7 as-is.
func TestComputeReturn_Bill(t *testing.T) {
	bills := []vatdb.ListBillsForVatReturnRow{{
		ID: [16]byte{3}, DatedOn: pgD("2026-04-12"), Reference: pgT("INV-9"),
		NetValueMinor: 50000, SalesTaxValueMinor: 10000,
	}}
	b, _, purchases := computeReturnAccrual(nil, nil, bills, nil)
	if b.Box4 != 10000 || b.Box7 != 50000 {
		t.Errorf("got Box4=%d Box7=%d, want 10000/50000", b.Box4, b.Box7)
	}
	if len(purchases) != 1 {
		t.Fatalf("want 1 purchase line, got %d", len(purchases))
	}
}

// Banking direction is set by the sign of gross_value_minor; VAT is a positive
// magnitude and net = |gross| − vat.
func TestComputeReturn_BankDirectionBySign(t *testing.T) {
	bank := []vatdb.ListExplanationsForVatReturnRow{
		{ID: [16]byte{4}, DatedOn: pgD("2026-04-15"), Description: pgT("Sale receipt"),
			GrossValueMinor: 12000, SalesTaxValueMinor: 2000, SalesTaxStatus: vatStatusTaxable}, // money in → output
		{ID: [16]byte{5}, DatedOn: pgD("2026-04-16"), Description: pgT("Stationery"),
			GrossValueMinor: -6000, SalesTaxValueMinor: 1000, SalesTaxStatus: vatStatusTaxable}, // money out → input
	}
	b, sales, purchases := computeReturnAccrual(nil, nil, nil, bank)
	if b.Box1 != 2000 || b.Box6 != 10000 {
		t.Errorf("money-in: got Box1=%d Box6=%d, want 2000/10000", b.Box1, b.Box6)
	}
	if b.Box4 != 1000 || b.Box7 != 5000 {
		t.Errorf("money-out: got Box4=%d Box7=%d, want 1000/5000", b.Box4, b.Box7)
	}
	if len(sales) != 1 || len(purchases) != 1 {
		t.Errorf("lines: got %d sales / %d purchases, want 1 / 1", len(sales), len(purchases))
	}
}

// OUT_OF_SCOPE rows are dropped entirely — not summed, not listed.
func TestComputeReturn_ExcludesOutOfScope(t *testing.T) {
	expenses := []vatdb.ListExpensesForVatReturnRow{{
		ID: [16]byte{6}, DatedOn: pgD("2026-04-20"), Description: "Bank charge",
		CategoryName: "Bank fees", NativeGrossValueMinor: 5000, NativeVatValueMinor: 0,
		VatStatus: vatStatusOutOfScope, EcStatus: ecUKNonEC,
	}}
	b, _, purchases := computeReturnAccrual(expenses, nil, nil, nil)
	if (b != vatBoxes{}) {
		t.Errorf("out-of-scope expense should contribute nothing, got %+v", b)
	}
	if len(purchases) != 0 {
		t.Errorf("out-of-scope expense should not be listed, got %d purchase lines", len(purchases))
	}
}

// CASH basis: an invoice receipt recognises a proportional share of the invoice's VAT.
func TestComputeReturnCash_InvoiceReceiptApportioned(t *testing.T) {
	// £1,200 invoice (net £1,000, VAT £200). A £600 part-receipt → half the VAT + net.
	receipts := []vatdb.ListInvoiceReceiptsForVatReturnRow{{
		ID: [16]byte{1}, DatedOn: pgD("2026-04-10"), Reference: pgT("001"),
		GrossValueMinor: 60000, InvoiceVatMinor: 20000, InvoiceTotalMinor: 120000,
	}}
	b, sales, purchases := computeReturnCash(nil, receipts, nil, nil)
	if b.Box1 != 10000 || b.Box6 != 50000 {
		t.Errorf("part-receipt: got Box1=%d Box6=%d, want 10000/50000", b.Box1, b.Box6)
	}
	if len(sales) != 1 || len(purchases) != 0 {
		t.Errorf("lines: got %d sales / %d purchases, want 1 / 0", len(sales), len(purchases))
	}
}

// CASH basis: a bill payment apportions the bill's VAT (positive input reclaim); a
// refund (positive gross) nets it back down.
func TestComputeReturnCash_BillPaymentAndRefund(t *testing.T) {
	// £1,200 bill (VAT £200, total £1,200). Pay in full (gross −120000) → Box4 £200,
	// Box7 £1,000. Then a £600 refund (gross +60000) → Box4 −£100, Box7 −£500.
	payments := []vatdb.ListBillPaymentsForVatReturnRow{
		{ID: [16]byte{2}, DatedOn: pgD("2026-04-12"), Reference: pgT("INV-9"),
			GrossValueMinor: -120000, BillVatMinor: 20000, BillTotalMinor: 120000},
		{ID: [16]byte{3}, DatedOn: pgD("2026-04-20"), Reference: pgT("INV-9"),
			GrossValueMinor: 60000, BillVatMinor: 20000, BillTotalMinor: 120000},
	}
	b, _, purchases := computeReturnCash(nil, nil, payments, nil)
	if b.Box4 != 10000 || b.Box7 != 50000 {
		t.Errorf("payment+refund net: got Box4=%d Box7=%d, want 10000/50000", b.Box4, b.Box7)
	}
	if len(purchases) != 2 {
		t.Errorf("want 2 purchase lines, got %d", len(purchases))
	}
}
