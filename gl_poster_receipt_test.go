package main

// gl_poster_receipt_test.go
// =============================================================================
// End-to-end test of the GL poster through the INVOICE_RECEIPT event: explaining a
// money-in bank line as a receipt against a SENT invoice posts Dr Bank / Cr Debtors,
// so the 681 Debtors control balance falls to the invoice's outstanding due. Deleting
// the receipt removes the entry. A FOREIGN-currency receipt additionally crystallises
// realised FX (Dr/Cr 390) — the cash's home value vs the booked receivable.
// =============================================================================

import (
	"context"
	"net/http"
	"testing"

	"github.com/operationfb/accounting-saas/internal/banking"
	"github.com/operationfb/accounting-saas/internal/invoices"
)

// newBankTxn inserts a money-in/out bank line and returns its id.
func newBankTxn(t *testing.T, ts *testServer, accID string, amountMinor int64) string {
	t.Helper()
	var id string
	if err := ts.pool.QueryRow(context.Background(),
		`INSERT INTO bank_transactions (organisation_id, bank_account_id, dated_on, amount_minor, status, source)
		 VALUES ($1, $2, CURRENT_DATE, $3, 'unexplained', 'manual') RETURNING id::text`,
		devOrgID, accID, amountMinor).Scan(&id); err != nil {
		t.Fatalf("new bank txn: %v", err)
	}
	return id
}

// liveReceiptExplID returns the org's single live INVOICE_RECEIPT explanation id.
func liveReceiptExplID(t *testing.T, ts *testServer) string {
	t.Helper()
	var id string
	if err := ts.pool.QueryRow(context.Background(),
		`SELECT id::text FROM bank_transaction_explanations
		 WHERE organisation_id = $1 AND type = 'INVOICE_RECEIPT' AND deleted_at IS NULL
		 ORDER BY created_at DESC LIMIT 1`, devOrgID).Scan(&id); err != nil {
		t.Fatalf("find receipt explanation: %v", err)
	}
	return id
}

// glAccountBalance is the org's SUM(base_amount_minor) on a nominal (a trial-balance cell).
func glAccountBalance(t *testing.T, ts *testServer, nominal string) int64 {
	t.Helper()
	var bal int64
	if err := ts.pool.QueryRow(context.Background(),
		`SELECT COALESCE(SUM(l.base_amount_minor), 0)
		   FROM gl_journal_lines l JOIN categories c ON c.id = l.account_id
		  WHERE l.organisation_id = $1 AND c.nominal_code = $2`, devOrgID, nominal).Scan(&bal); err != nil {
		t.Fatalf("account balance %s: %v", nominal, err)
	}
	return bal
}

// issueGLInvoice creates + issues an invoice (posts INVOICE_SENT) and returns its id.
func issueGLInvoice(t *testing.T, ts *testServer, authHeader, ccy, rate string) string {
	t.Helper()
	contactID := createContactAs(t, ts, devUserID, devOrgID)
	rec := postInvoice(t, ts, authHeader, invoices.CreateInvoiceRequest{
		ContactID: contactID, DatedOn: today(), Reference: randomRef(), Currency: ccy, ExchangeRate: rate,
		Items: []invoices.InvoiceItemRequest{{Description: "Consulting", Quantity: "1", Price: "100.00", SalesTaxRate: "20"}},
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create invoice: %d %s", rec.Code, rec.Body.String())
	}
	inv := decodeInvoice(t, rec.Body.Bytes())
	if s := statusInvoiceReq(t, ts, inv.ID, authHeader, "issue"); s.Code != http.StatusOK {
		t.Fatalf("issue invoice: %d %s", s.Code, s.Body.String())
	}
	return inv.ID
}

func TestInvoiceReceiptPostsToLedger_GBP(t *testing.T) {
	ts := newTestServer(t)
	ctx := context.Background()
	authHeader := bearer(t, ts, devUserID, devOrgID)
	userID, orgID := mustUUID(t, devUserID), mustUUID(t, devOrgID)

	invID := issueGLInvoice(t, ts, authHeader, "GBP", "") // posts Dr Debtors(681) +12000

	acc, err := ts.bankingService.CreateBankAccount(ctx, userID, orgID, bankReq("GL Receipt", nil)) // GBP = home
	if err != nil {
		t.Fatalf("create bank account: %v", err)
	}
	txnID := newBankTxn(t, ts, acc.ID, 12000) // £120.00 money in

	t.Cleanup(func() {
		bg := context.Background()
		purgeGLEntries(bg, t, ts.pool, `organisation_id=$1 AND (source_id=$2 OR source_type='INVOICE_RECEIPT')`, devOrgID, invID)
		ts.pool.Exec(bg, `DELETE FROM categories WHERE organisation_id=$1 AND bank_account_id=$2`, devOrgID, acc.ID)
		ts.pool.Exec(bg, `DELETE FROM bank_transaction_explanations WHERE bank_transaction_id=$1`, txnID)
		ts.pool.Exec(bg, `DELETE FROM bank_transactions WHERE bank_account_id=$1`, acc.ID)
		cleanupBankAccount(t, ts, acc.ID)
		cleanupInvoice(t, ts, invID)
	})

	debtorsAfterIssue := glAccountBalance(t, ts, "681") // includes our +12000

	// Explain the line as a full INVOICE_RECEIPT.
	if _, err := ts.bankingService.CreateExplanation(ctx, userID, orgID, acc.ID, txnID, banking.CreateExplanationRequest{
		Type: "INVOICE_RECEIPT", Amount: "120.00", PaidInvoiceID: &invID,
	}); err != nil {
		t.Fatalf("explain invoice receipt: %v", err)
	}

	// The receipt entry: Dr Bank 750-x +12000, Cr Debtors 681 −12000, balanced.
	lines := glLinesForSource(t, ts, "INVOICE_RECEIPT", liveReceiptExplID(t, ts))
	if len(lines) != 2 {
		t.Fatalf("expected 2 receipt lines, got %d: %v", len(lines), lines)
	}
	assertLine(t, lines, "681", "GBP", -12000, -12000) // Cr Debtors
	bank, ok := bankSubLine(lines)
	if !ok {
		t.Fatalf("expected a 750-x bank line, got %v", lines)
	}
	if bank.amount != 12000 || bank.base != 12000 || bank.currency != "GBP" {
		t.Errorf("bank line = %+v, want +12000/+12000 GBP", bank)
	}

	// Payoff: the receipt reduced the Debtors control by the full amount (so the
	// fully-paid invoice no longer contributes a receivable).
	if delta := debtorsAfterIssue - glAccountBalance(t, ts, "681"); delta != 12000 {
		t.Errorf("Debtors should drop by 12000 on full receipt, dropped %d", delta)
	}

	// Deleting the receipt REVERSES its entry (append-only — not deleted) and restores
	// Debtors. The original lines remain, a reversal exists, and the source nets to zero.
	explID := liveReceiptExplID(t, ts)
	if _, err := ts.bankingService.DeleteExplanation(ctx, userID, orgID, acc.ID, txnID, explID); err != nil {
		t.Fatalf("delete receipt: %v", err)
	}
	if net := glSourceNetBase(t, ts, "INVOICE_RECEIPT", explID); net != 0 {
		t.Errorf("receipt source net base should be 0 after delete (original + reversal), got %d", net)
	}
	if reversals := glReversalCount(t, ts, "INVOICE_RECEIPT", explID); reversals != 1 {
		t.Errorf("expected 1 reversal entry after deleting the receipt, got %d", reversals)
	}
	if delta := debtorsAfterIssue - glAccountBalance(t, ts, "681"); delta != 0 {
		t.Errorf("Debtors should be restored after deleting the receipt, delta %d", delta)
	}
}

// invoiceDue returns an invoice's due_value_minor (in the invoice's currency).
func invoiceDue(t *testing.T, ts *testServer, invID string) int64 {
	t.Helper()
	var due int64
	if err := ts.pool.QueryRow(context.Background(),
		`SELECT due_value_minor FROM invoices WHERE id = $1`, invID).Scan(&due); err != nil {
		t.Fatalf("invoice due: %v", err)
	}
	return due
}

// TestInvoiceReceiptForeignCurrencyRealisedLoss settles a EUR invoice in full from a EUR
// bank account at a rate that has moved since the invoice was booked. The receipt posts a
// balanced 3-leg journal: Dr Bank (EUR), Cr Debtors (EUR, at the booking rate), and the
// realised FX difference to 390. EUR weakened (0.86 → 0.80), so the home cash received is
// worth LESS than the booked receivable → a realised LOSS.
func TestInvoiceReceiptForeignCurrencyRealisedLoss(t *testing.T) {
	ts := newTestServer(t)
	ctx := context.Background()
	authHeader := bearer(t, ts, devUserID, devOrgID)
	userID, orgID := mustUUID(t, devUserID), mustUUID(t, devOrgID)

	// Invoice €120 booked at 0.86 ⇒ native (home) receivable 10320p; receipt today at 0.80.
	invID := issueGLInvoice(t, ts, authHeader, "EUR", "0.86")
	seedRate(t, ts, "EUR", today(), "0.80")

	acc, err := ts.bankingService.CreateBankAccount(ctx, userID, orgID, bankReq("EUR acct", func(r *banking.CreateBankAccountRequest) { r.Currency = "EUR" }))
	if err != nil {
		t.Fatalf("create EUR bank account: %v", err)
	}
	txnID := newBankTxn(t, ts, acc.ID, 12000) // €120.00 money in

	t.Cleanup(func() {
		bg := context.Background()
		purgeGLEntries(bg, t, ts.pool, `organisation_id=$1 AND (source_id=$2 OR source_type='INVOICE_RECEIPT')`, devOrgID, invID)
		ts.pool.Exec(bg, `DELETE FROM categories WHERE organisation_id=$1 AND bank_account_id=$2`, devOrgID, acc.ID)
		ts.pool.Exec(bg, `DELETE FROM bank_transaction_explanations WHERE bank_transaction_id=$1`, txnID)
		ts.pool.Exec(bg, `DELETE FROM bank_transactions WHERE bank_account_id=$1`, acc.ID)
		cleanupBankAccount(t, ts, acc.ID)
		cleanupInvoice(t, ts, invID)
	})

	debtorsAfterIssue := glAccountBalance(t, ts, "681") // includes +10320 from this invoice
	fxBefore := glAccountBalance(t, ts, "390")

	if _, err := ts.bankingService.CreateExplanation(ctx, userID, orgID, acc.ID, txnID, banking.CreateExplanationRequest{
		Type: "INVOICE_RECEIPT", Amount: "120.00", PaidInvoiceID: &invID,
	}); err != nil {
		t.Fatalf("explain EUR invoice receipt: %v", err)
	}

	// 3 legs: Dr Bank 750-x +12000 EUR / +9600 home; Cr Debtors 681 −12000 EUR / −10320 home;
	// Dr 390 realised loss +720 home. Balances on base: 9600 − 10320 + 720 = 0.
	lines := glLinesForSource(t, ts, "INVOICE_RECEIPT", liveReceiptExplID(t, ts))
	if len(lines) != 3 {
		t.Fatalf("expected 3 receipt lines (bank/debtors/FX), got %d: %v", len(lines), lines)
	}
	assertLine(t, lines, "681", "EUR", -12000, -10320) // Cr Debtors at the booking rate
	assertLine(t, lines, "390", "GBP", 720, 720)       // Dr realised FX loss (home)
	bank, ok := bankSubLine(lines)
	if !ok {
		t.Fatalf("expected a 750-x bank line, got %v", lines)
	}
	if bank.amount != 12000 || bank.base != 9600 || bank.currency != "EUR" {
		t.Errorf("bank line = %+v, want +12000 EUR / +9600 home", bank)
	}

	var base int64
	for _, l := range lines {
		base += l.base
	}
	if base != 0 {
		t.Errorf("receipt entry must balance on base, got Σ = %d", base)
	}

	// Debtors relieved by the full BOOKED receivable (10320), and the invoice is fully paid.
	if delta := debtorsAfterIssue - glAccountBalance(t, ts, "681"); delta != 10320 {
		t.Errorf("Debtors should drop by the booked 10320 on full receipt, dropped %d", delta)
	}
	if got := glAccountBalance(t, ts, "390") - fxBefore; got != 720 {
		t.Errorf("realised FX (390) should be +720 loss, got %d", got)
	}
	if due := invoiceDue(t, ts, invID); due != 0 {
		t.Errorf("invoice should be fully paid (due 0), got %d", due)
	}
}

// TestInvoiceReceiptForeignCurrencyResidualClosesToZero pays a EUR invoice in TWO partial
// receipts. The cumulative debtor relief must sum to the booked native total EXACTLY (the
// rounding crumb absorbed by the residual), so the home receivable closes to zero.
func TestInvoiceReceiptForeignCurrencyResidualClosesToZero(t *testing.T) {
	ts := newTestServer(t)
	ctx := context.Background()
	authHeader := bearer(t, ts, devUserID, devOrgID)
	userID, orgID := mustUUID(t, devUserID), mustUUID(t, devOrgID)

	invID := issueGLInvoice(t, ts, authHeader, "EUR", "0.86") // native receivable 10320p
	seedRate(t, ts, "EUR", today(), "0.80")

	acc, err := ts.bankingService.CreateBankAccount(ctx, userID, orgID, bankReq("EUR acct", func(r *banking.CreateBankAccountRequest) { r.Currency = "EUR" }))
	if err != nil {
		t.Fatalf("create EUR bank account: %v", err)
	}
	txnID := newBankTxn(t, ts, acc.ID, 12000) // €120 money in, split into two receipts

	t.Cleanup(func() {
		bg := context.Background()
		purgeGLEntries(bg, t, ts.pool, `organisation_id=$1 AND (source_id=$2 OR source_type='INVOICE_RECEIPT')`, devOrgID, invID)
		ts.pool.Exec(bg, `DELETE FROM categories WHERE organisation_id=$1 AND bank_account_id=$2`, devOrgID, acc.ID)
		ts.pool.Exec(bg, `DELETE FROM bank_transaction_explanations WHERE bank_transaction_id=$1`, txnID)
		ts.pool.Exec(bg, `DELETE FROM bank_transactions WHERE bank_account_id=$1`, acc.ID)
		cleanupBankAccount(t, ts, acc.ID)
		cleanupInvoice(t, ts, invID)
	})

	debtorsAfterIssue := glAccountBalance(t, ts, "681")

	for _, amt := range []string{"50.00", "70.00"} {
		if _, err := ts.bankingService.CreateExplanation(ctx, userID, orgID, acc.ID, txnID, banking.CreateExplanationRequest{
			Type: "INVOICE_RECEIPT", Amount: amt, PaidInvoiceID: &invID,
		}); err != nil {
			t.Fatalf("explain receipt %s: %v", amt, err)
		}
	}

	// The two reliefs (4300 + 6020) sum to the booked 10320 exactly → Debtors closes.
	if delta := debtorsAfterIssue - glAccountBalance(t, ts, "681"); delta != 10320 {
		t.Errorf("cumulative debtor relief must equal the booked 10320, dropped %d", delta)
	}
	if due := invoiceDue(t, ts, invID); due != 0 {
		t.Errorf("invoice should be fully paid (due 0), got %d", due)
	}
}

// bankSubLine returns the single 750-x bank line from a receipt's lines.
func bankSubLine(lines map[string]glLine) (glLine, bool) {
	for nominal, l := range lines {
		if len(nominal) >= 4 && nominal[:4] == "750-" {
			return l, true
		}
	}
	return glLine{}, false
}

// glLinesForSource2 is glLinesForSource returning just the line count (post-delete checks).
func glLinesForSource2(t *testing.T, ts *testServer, sourceType, sourceID string) int {
	return len(glLinesForSource(t, ts, sourceType, sourceID))
}
