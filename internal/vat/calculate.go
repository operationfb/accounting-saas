package vat

// calculate.go
// =============================================================================
// The pure VAT-return calculation engine (invoice/accrual basis). It takes the
// already-stored VAT/net amounts fetched from the four sources and routes each
// into the 9 UK HMRC boxes. No DB and no clock here — `routeToBoxes` is a pure
// function of (direction, vat_status, ec_status, vat, net), so the box maths is
// unit-tested directly.
//
// Each source is normalised by `…ToLine` into a `vatLine` carrying a **signed**
// (vat, net) — positive for a normal sale/purchase. Because it's signed (not abs),
// a credit note / refund (a negative document) nets correctly with no special case.
// Sign transforms per source (verified against the writing services):
//   - invoices: amounts stored positive (a receivable)            → as-is, output
//   - bills:    entered VAT-inclusive total, positive for a normal bill → as-is, input
//   - expenses: entered VAT-inclusive amount, positive (like a bill) → as-is, input
//               (the frontend sends the positive value; a refund/credit, stored
//               negative, then nets correctly)
//   - bank:     gross is SIGNED (+in/−out), VAT a positive magnitude → direction by
//               sign(gross); net = |gross| − vat
// =============================================================================

import (
	"github.com/jackc/pgx/v5/pgtype"

	vatdb "github.com/operationfb/accounting-saas/db/vat"
	"github.com/operationfb/accounting-saas/money"
)

// vat_status values (mirror the DB CHECK on expenses/bank_transaction_explanations).
const (
	vatStatusTaxable    = "TAXABLE"
	vatStatusExempt     = "EXEMPT"
	vatStatusOutOfScope = "OUT_OF_SCOPE"
)

// ec_status values (mirror the DB CHECK on expenses; banking uses the same set).
const (
	ecUKNonEC       = "UK_NON_EC"
	ecGoods         = "EC_GOODS"
	ecServices      = "EC_SERVICES"
	ecReverseCharge = "REVERSE_CHARGE"
)

const (
	dirOutput = "output" // a sale
	dirInput  = "input"  // a purchase
)

// vatBoxes holds the 9 HMRC boxes in pence (int64). Boxes 1–5 are VAT amounts;
// boxes 6–9 are net values. Box5 is SIGNED (negative = reclaim/refund due to you).
type vatBoxes struct {
	Box1, Box2, Box3, Box4, Box5 int64
	Box6, Box7, Box8, Box9       int64
}

// vatLine is one normalised contributing transaction. The display fields feed the
// Full Report; (Direction, VatStatus, EcStatus, NetMinor, VatMinor) feed the boxes.
// ID is the underlying record's UUID, set only for sources that have a detail
// route (expense/invoice/bill) so the Full Report can link each line to it; "" for
// bank/cash lines, which have no single addressable record.
type vatLine struct {
	ID          string // underlying record UUID ("" = not linkable)
	Date        string // YYYY-MM-DD
	Source      string // "invoice" | "expense" | "bill" | "bank"
	Description string
	Reference   string
	Direction   string
	VatStatus   string
	EcStatus    string
	NetMinor    int64
	VatMinor    int64
}

// routeToBoxes applies one normalised line's (vat, net) to the 9 boxes per the UK
// Standard Scheme with full EC + reverse-charge handling.
//   - OUT_OF_SCOPE contributes nothing; EXEMPT contributes net only (no VAT).
//   - Reverse charge / EC services on a PURCHASE self-account: notional VAT into
//     BOTH Box 1 and Box 4, net into BOTH Box 6 and Box 7.
//   - EC goods acquisition: VAT into Box 2 + Box 4, net into Box 9 + Box 7.
//   - EC goods dispatch (a sale): net into Box 6 + Box 8, zero-rated (no VAT).
func routeToBoxes(direction, vatStatus, ecStatus string, vatMinor, netMinor int64, b *vatBoxes) {
	if vatStatus == vatStatusOutOfScope {
		return
	}
	hasVat := vatStatus == vatStatusTaxable // EXEMPT ⇒ net-only
	if ecStatus == "" {
		ecStatus = ecUKNonEC
	}

	switch direction {
	case dirOutput:
		switch ecStatus {
		case ecGoods: // dispatch of goods to the EU (NI protocol) — zero-rated
			b.Box6 += netMinor
			b.Box8 += netMinor
		case ecReverseCharge, ecServices: // supply where the customer self-accounts
			b.Box6 += netMinor
		default: // UK_NON_EC
			b.Box6 += netMinor
			if hasVat {
				b.Box1 += vatMinor
			}
		}
	case dirInput:
		switch ecStatus {
		case ecGoods: // acquisition of goods from the EU (NI protocol)
			b.Box7 += netMinor
			b.Box9 += netMinor
			if hasVat {
				b.Box2 += vatMinor
				b.Box4 += vatMinor
			}
		case ecReverseCharge, ecServices: // reverse-charge purchase
			b.Box7 += netMinor
			b.Box6 += netMinor
			if hasVat {
				b.Box1 += vatMinor
				b.Box4 += vatMinor
			}
		default: // UK_NON_EC
			b.Box7 += netMinor
			if hasVat {
				b.Box4 += vatMinor
			}
		}
	}
}

// computeFromLines routes a set of normalised lines into the 9 boxes and splits them
// into sales / purchases for the Full Report. OUT_OF_SCOPE lines are dropped entirely
// (not shown, not summed). Boxes 3 and 5 are derived; Box5 is signed (negative =
// reclaim). Shared by the accrual and cash builders below.
func computeFromLines(lines []vatLine) (vatBoxes, []vatLine, []vatLine) {
	var b vatBoxes
	sales := []vatLine{}
	purchases := []vatLine{}
	for _, l := range lines {
		if l.VatStatus == vatStatusOutOfScope {
			continue
		}
		routeToBoxes(l.Direction, l.VatStatus, l.EcStatus, l.VatMinor, l.NetMinor, &b)
		if l.Direction == dirOutput {
			sales = append(sales, l)
		} else {
			purchases = append(purchases, l)
		}
	}
	b.Box3 = b.Box1 + b.Box2
	b.Box5 = b.Box3 - b.Box4
	return b, sales, purchases
}

// computeReturnAccrual builds the INVOICE/ACCRUAL return: the documents counted by
// their date (invoices SENT, expenses, bills) + direct-category bank explanations.
func computeReturnAccrual(
	expenses []vatdb.ListExpensesForVatReturnRow,
	invoices []vatdb.ListInvoicesForVatReturnRow,
	bills []vatdb.ListBillsForVatReturnRow,
	bankLines []vatdb.ListExplanationsForVatReturnRow,
) (vatBoxes, []vatLine, []vatLine) {
	lines := make([]vatLine, 0, len(invoices)+len(expenses)+len(bills)+len(bankLines))
	for _, r := range invoices {
		lines = append(lines, invoiceToLine(r))
	}
	for _, r := range expenses {
		lines = append(lines, expenseToLine(r))
	}
	for _, r := range bills {
		lines = append(lines, billToLine(r))
	}
	for _, r := range bankLines {
		lines = append(lines, explanationToLine(r))
	}
	return computeFromLines(lines)
}

// computeReturnCash builds the CASH return: invoices and bills are recognised via the
// bank transactions that SETTLE them (receipts / payments, with the document's VAT
// apportioned to the amount that moved) rather than by document date. Expenses and
// direct-category bank explanations are identical to the accrual basis.
func computeReturnCash(
	expenses []vatdb.ListExpensesForVatReturnRow,
	receipts []vatdb.ListInvoiceReceiptsForVatReturnRow,
	payments []vatdb.ListBillPaymentsForVatReturnRow,
	bankLines []vatdb.ListExplanationsForVatReturnRow,
) (vatBoxes, []vatLine, []vatLine) {
	lines := make([]vatLine, 0, len(receipts)+len(expenses)+len(payments)+len(bankLines))
	for _, r := range receipts {
		lines = append(lines, invoiceReceiptToLine(r))
	}
	for _, r := range expenses {
		lines = append(lines, expenseToLine(r))
	}
	for _, r := range payments {
		lines = append(lines, billPaymentToLine(r))
	}
	for _, r := range bankLines {
		lines = append(lines, explanationToLine(r))
	}
	return computeFromLines(lines)
}

// =============================================================================
// Source → line mappers (apply the verified sign transforms)
// =============================================================================

func invoiceToLine(r vatdb.ListInvoicesForVatReturnRow) vatLine {
	ref := textOr(r.Reference, "")
	return vatLine{
		ID:          r.ID.String(),
		Date:        dateStr(r.DatedOn),
		Source:      "invoice",
		Description: descOr(ref, "Invoice"),
		Reference:   ref,
		Direction:   dirOutput,
		VatStatus:   vatStatusTaxable, // invoices carry no status axes → UK-standard
		EcStatus:    ecUKNonEC,
		NetMinor:    r.NetValueMinor,
		VatMinor:    r.SalesTaxValueMinor,
	}
}

func billToLine(r vatdb.ListBillsForVatReturnRow) vatLine {
	ref := textOr(r.Reference, "")
	return vatLine{
		ID:          r.ID.String(),
		Date:        dateStr(r.DatedOn),
		Source:      "bill",
		Description: descOr(textOr(r.Comments, ""), descOr(ref, "Bill")),
		Reference:   ref,
		Direction:   dirInput,
		VatStatus:   vatStatusTaxable, // bills carry no status axes → UK-standard
		EcStatus:    ecUKNonEC,
		NetMinor:    r.NetValueMinor,
		VatMinor:    r.SalesTaxValueMinor,
	}
}

func expenseToLine(r vatdb.ListExpensesForVatReturnRow) vatLine {
	gross := int64(r.NativeGrossValueMinor)
	vat := int64(r.NativeVatValueMinor)
	// Expenses are stored POSITIVE (the entered VAT-inclusive amount), like a bill,
	// so net = gross − vat as-is. (A refund/credit, stored negative, then nets.)
	return vatLine{
		ID:          r.ID.String(),
		Date:        dateStr(r.DatedOn),
		Source:      "expense",
		Description: descOr(r.Description, r.CategoryName),
		Reference:   textOr(r.SupplierName, ""),
		Direction:   dirInput,
		VatStatus:   r.VatStatus,
		EcStatus:    r.EcStatus,
		NetMinor:    gross - vat,
		VatMinor:    vat,
	}
}

func explanationToLine(r vatdb.ListExplanationsForVatReturnRow) vatLine {
	// gross is signed: positive = money in (a sale/output), negative = money out
	// (a purchase/input). VAT is stored as a positive magnitude.
	direction := dirInput
	gross := r.GrossValueMinor
	if gross > 0 {
		direction = dirOutput
	}
	if gross < 0 {
		gross = -gross
	}
	return vatLine{
		Date:        dateStr(r.DatedOn),
		Source:      "bank",
		Description: textOr(r.Description, "Bank transaction"),
		Direction:   direction,
		VatStatus:   r.SalesTaxStatus,
		EcStatus:    ecOr(r.EcStatus),
		NetMinor:    gross - r.SalesTaxValueMinor,
		VatMinor:    r.SalesTaxValueMinor,
	}
}

// invoiceReceiptToLine (CASH basis) apportions the linked invoice's VAT to the
// received amount. The receipt's gross is positive (money in); output direction.
// net + vat = the receipt, so a part-payment recognises a proportional share.
func invoiceReceiptToLine(r vatdb.ListInvoiceReceiptsForVatReturnRow) vatLine {
	gross := r.GrossValueMinor
	vat := money.Apportion(gross, r.InvoiceVatMinor, r.InvoiceTotalMinor)
	ref := textOr(r.Reference, "")
	return vatLine{
		Date:        dateStr(r.DatedOn),
		Source:      "invoice",
		Description: descOr(ref, "Invoice receipt"),
		Reference:   ref,
		Direction:   dirOutput,
		VatStatus:   vatStatusTaxable, // invoices are always UK-standard
		EcStatus:    ecUKNonEC,
		NetMinor:    gross - vat,
		VatMinor:    vat,
	}
}

// billPaymentToLine (CASH basis) apportions the linked bill's VAT to the amount paid.
// The payment's gross is SIGNED (− out / + refund), so the apportioned share is
// negated → a payment becomes a positive input reclaim and a refund nets it down.
func billPaymentToLine(r vatdb.ListBillPaymentsForVatReturnRow) vatLine {
	gross := r.GrossValueMinor
	vat := -money.Apportion(gross, r.BillVatMinor, r.BillTotalMinor)
	ref := textOr(r.Reference, "")
	return vatLine{
		Date:        dateStr(r.DatedOn),
		Source:      "bill",
		Description: descOr(textOr(r.Comments, ""), descOr(ref, "Bill payment")),
		Reference:   ref,
		Direction:   dirInput,
		VatStatus:   vatStatusTaxable,
		EcStatus:    ecUKNonEC,
		NetMinor:    -gross - vat,
		VatMinor:    vat,
	}
}

// =============================================================================
// Small helpers
// =============================================================================

// dateStr renders a DATE as YYYY-MM-DD ("" when NULL).
func dateStr(d pgtype.Date) string {
	if !d.Valid {
		return ""
	}
	return d.Time.Format("2006-01-02")
}

// textOr returns the text value, or def when NULL/blank.
func textOr(t pgtype.Text, def string) string {
	if t.Valid && t.String != "" {
		return t.String
	}
	return def
}

// ecOr returns the ec_status, defaulting to UK_NON_EC when NULL/blank (banking's
// ec_status is nullable; invoices/bills have no column).
func ecOr(t pgtype.Text) string {
	if t.Valid && t.String != "" {
		return t.String
	}
	return ecUKNonEC
}

// descOr returns s, or fallback when s is blank — for a sensible line label.
func descOr(s, fallback string) string {
	if s != "" {
		return s
	}
	return fallback
}

// roundToWholePoundMinor rounds a pence amount to the nearest whole pound (HMRC
// rounds boxes 6–9 to whole pounds). Half rounds away from zero.
func roundToWholePoundMinor(minor int64) int64 {
	if minor >= 0 {
		return ((minor + 50) / 100) * 100
	}
	return -((((-minor) + 50) / 100) * 100)
}

// boxesToResponse renders the pence boxes into the DTO's pound strings: boxes 1–5
// to 2dp, boxes 6–9 rounded to whole pounds (then rendered, so "1656.00").
func boxesToResponse(b vatBoxes, resp *VatReturnResponse) {
	resp.Box1 = money.MinorToPounds(b.Box1)
	resp.Box2 = money.MinorToPounds(b.Box2)
	resp.Box3 = money.MinorToPounds(b.Box3)
	resp.Box4 = money.MinorToPounds(b.Box4)
	resp.Box5 = money.MinorToPounds(b.Box5)
	resp.Box6 = money.MinorToPounds(roundToWholePoundMinor(b.Box6))
	resp.Box7 = money.MinorToPounds(roundToWholePoundMinor(b.Box7))
	resp.Box8 = money.MinorToPounds(roundToWholePoundMinor(b.Box8))
	resp.Box9 = money.MinorToPounds(roundToWholePoundMinor(b.Box9))
	resp.NetDue = money.MinorToPounds(b.Box5)
	resp.IsReclaim = b.Box5 < 0
}

// linesToResponse renders internal vatLines into the DTO's line shape (exact 2dp).
func linesToResponse(lines []vatLine) []VatReturnLineResponse {
	out := make([]VatReturnLineResponse, 0, len(lines))
	for _, l := range lines {
		out = append(out, VatReturnLineResponse{
			ID:          l.ID,
			Date:        l.Date,
			Source:      l.Source,
			Description: l.Description,
			Reference:   l.Reference,
			Net:         money.MinorToPounds(l.NetMinor),
			Vat:         money.MinorToPounds(l.VatMinor),
		})
	}
	return out
}
