package main

// gl_poster_receipt_test.go
// =============================================================================
// End-to-end test of the GL poster through the INVOICE_RECEIPT event: explaining a
// money-in bank line as a receipt against a SENT invoice posts Dr Bank / Cr Debtors,
// so the 681 Debtors control balance falls to the invoice's outstanding due. Deleting
// the receipt removes the entry; a foreign-currency receipt posts nothing (FX deferred).
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
		ts.pool.Exec(bg, `DELETE FROM gl_journal_entries WHERE organisation_id=$1 AND (source_id=$2 OR source_type='INVOICE_RECEIPT')`, devOrgID, invID)
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

	// Deleting the receipt removes its entry and restores Debtors.
	explID := liveReceiptExplID(t, ts)
	if _, err := ts.bankingService.DeleteExplanation(ctx, userID, orgID, acc.ID, txnID, explID); err != nil {
		t.Fatalf("delete receipt: %v", err)
	}
	if got := glLinesForSource2(t, ts, "INVOICE_RECEIPT", explID); got != 0 {
		t.Errorf("expected receipt entry removed after delete, got %d lines", got)
	}
	if delta := debtorsAfterIssue - glAccountBalance(t, ts, "681"); delta != 0 {
		t.Errorf("Debtors should be restored after deleting the receipt, delta %d", delta)
	}
}

func TestInvoiceReceiptForeignCurrencySkipsLedger(t *testing.T) {
	ts := newTestServer(t)
	ctx := context.Background()
	authHeader := bearer(t, ts, devUserID, devOrgID)
	userID, orgID := mustUUID(t, devUserID), mustUUID(t, devOrgID)

	invID := issueGLInvoice(t, ts, authHeader, "EUR", "0.86")

	acc, err := ts.bankingService.CreateBankAccount(ctx, userID, orgID, bankReq("EUR acct", func(r *banking.CreateBankAccountRequest) { r.Currency = "EUR" }))
	if err != nil {
		t.Fatalf("create EUR bank account: %v", err)
	}
	txnID := newBankTxn(t, ts, acc.ID, 12000) // €120.00 money in

	t.Cleanup(func() {
		bg := context.Background()
		ts.pool.Exec(bg, `DELETE FROM gl_journal_entries WHERE organisation_id=$1 AND (source_id=$2 OR source_type='INVOICE_RECEIPT')`, devOrgID, invID)
		ts.pool.Exec(bg, `DELETE FROM bank_transaction_explanations WHERE bank_transaction_id=$1`, txnID)
		ts.pool.Exec(bg, `DELETE FROM bank_transactions WHERE bank_account_id=$1`, acc.ID)
		cleanupBankAccount(t, ts, acc.ID)
		cleanupInvoice(t, ts, invID)
	})

	if _, err := ts.bankingService.CreateExplanation(ctx, userID, orgID, acc.ID, txnID, banking.CreateExplanationRequest{
		Type: "INVOICE_RECEIPT", Amount: "120.00", PaidInvoiceID: &invID,
	}); err != nil {
		t.Fatalf("explain EUR invoice receipt: %v", err)
	}

	// Foreign-currency receipt: realized FX deferred → no GL entry posted for it.
	if got := glLinesForSource2(t, ts, "INVOICE_RECEIPT", liveReceiptExplID(t, ts)); got != 0 {
		t.Errorf("foreign-currency receipt should not post a GL entry, got %d lines", got)
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
