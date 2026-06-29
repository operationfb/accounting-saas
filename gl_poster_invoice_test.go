package main

// gl_poster_invoice_test.go
// =============================================================================
// End-to-end test of the GL poster through the INVOICE_SENT event (real Postgres):
// issuing an invoice posts a balanced multi-currency journal entry (Dr Debtors / Cr
// Sales + VAT control); reopening removes it; the Σ base = 0 trigger rejects an
// unbalanced entry. Drives the assembled invoices service via its HTTP handlers.
// =============================================================================

import (
	"context"
	"net/http"
	"testing"

	"github.com/operationfb/accounting-saas/internal/invoices"
)

// glLine is one posted journal line, keyed by its account's nominal code.
type glLine struct {
	currency string
	amount   int64
	base     int64
}

// glLinesForSource returns the live journal lines for a source event, keyed by the
// posting account's nominal_code.
func glLinesForSource(t *testing.T, ts *testServer, sourceType, sourceID string) map[string]glLine {
	t.Helper()
	rows, err := ts.pool.Query(context.Background(),
		`SELECT c.nominal_code, l.currency, l.amount_minor, l.base_amount_minor
		   FROM gl_journal_lines l
		   JOIN gl_journal_entries e ON e.id = l.journal_entry_id
		   JOIN categories c ON c.id = l.account_id
		  WHERE e.organisation_id = $1 AND e.source_type = $2 AND e.source_id = $3 AND NOT e.is_reversal
		  ORDER BY c.nominal_code`,
		devOrgID, sourceType, sourceID)
	if err != nil {
		t.Fatalf("query gl lines: %v", err)
	}
	defer rows.Close()
	out := map[string]glLine{}
	for rows.Next() {
		var nominal string
		var l glLine
		if err := rows.Scan(&nominal, &l.currency, &l.amount, &l.base); err != nil {
			t.Fatalf("scan gl line: %v", err)
		}
		out[nominal] = l
	}
	return out
}

// cleanupGLForInvoice removes the journal entry (lines cascade) — the source link is a
// soft reference, so deleting the invoice doesn't cascade to the GL.
func cleanupGLForInvoice(t *testing.T, ts *testServer, invoiceID string) {
	_, _ = ts.pool.Exec(context.Background(),
		`DELETE FROM gl_journal_entries WHERE organisation_id = $1 AND source_type = 'INVOICE' AND source_id = $2`,
		devOrgID, invoiceID)
}

func issueInvoice(t *testing.T, ts *testServer, ccy, rate string) string {
	t.Helper()
	authHeader := bearer(t, ts, devUserID, devOrgID)
	contactID := createContactAs(t, ts, devUserID, devOrgID)
	rec := postInvoice(t, ts, authHeader, invoices.CreateInvoiceRequest{
		ContactID:    contactID,
		DatedOn:      today(),
		Reference:    randomRef(),
		Currency:     ccy,
		ExchangeRate: rate,
		Items:        []invoices.InvoiceItemRequest{{Description: "Consulting", Quantity: "1", Price: "100.00", SalesTaxRate: "20"}},
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create invoice: status %d body %s", rec.Code, rec.Body.String())
	}
	inv := decodeInvoice(t, rec.Body.Bytes())
	t.Cleanup(func() { cleanupGLForInvoice(t, ts, inv.ID); cleanupInvoice(t, ts, inv.ID) })

	if srec := statusInvoiceReq(t, ts, inv.ID, authHeader, "issue"); srec.Code != http.StatusOK {
		t.Fatalf("issue invoice: status %d body %s", srec.Code, srec.Body.String())
	}
	return inv.ID
}

// want is a small assertion helper for a posted line.
func assertLine(t *testing.T, lines map[string]glLine, nominal, ccy string, amount, base int64) {
	t.Helper()
	l, ok := lines[nominal]
	if !ok {
		t.Fatalf("expected a journal line on nominal %s; got %v", nominal, lines)
	}
	if l.currency != ccy || l.amount != amount || l.base != base {
		t.Errorf("nominal %s: got {%s %d/%d}, want {%s %d/%d}", nominal, l.currency, l.amount, l.base, ccy, amount, base)
	}
}

func TestInvoiceSentPostsBalancedEntry_GBP(t *testing.T) {
	ts := newTestServer(t)
	invID := issueInvoice(t, ts, "GBP", "")

	lines := glLinesForSource(t, ts, "INVOICE", invID)
	if len(lines) != 3 {
		t.Fatalf("expected 3 journal lines, got %d: %v", len(lines), lines)
	}
	// Dr Debtors (681) +120.00, Cr Sales (001) −100.00, Cr VAT control (817) −20.00.
	// GBP invoice: base == amount on every line.
	assertLine(t, lines, "681", "GBP", 12000, 12000)
	assertLine(t, lines, "001", "GBP", -10000, -10000)
	assertLine(t, lines, "817", "GBP", -2000, -2000)

	var sum int64
	for _, l := range lines {
		sum += l.base
	}
	if sum != 0 {
		t.Errorf("entry does not balance in base: Σ = %d", sum)
	}
}

func TestInvoiceSentPostsBalancedEntry_EUR(t *testing.T) {
	ts := newTestServer(t)
	// EUR invoice, rate 0.86 GBP per EUR. native = round(amount × 0.86).
	invID := issueInvoice(t, ts, "EUR", "0.86")

	lines := glLinesForSource(t, ts, "INVOICE", invID)
	if len(lines) != 3 {
		t.Fatalf("expected 3 journal lines, got %d: %v", len(lines), lines)
	}
	// Transaction amounts in EUR; base amounts in GBP (the home currency).
	assertLine(t, lines, "681", "EUR", 12000, 10320) // €120.00 → £103.20
	assertLine(t, lines, "001", "EUR", -10000, -8600)
	assertLine(t, lines, "817", "EUR", -2000, -1720)

	var sumBase, sumTxn int64
	for _, l := range lines {
		sumBase += l.base
		sumTxn += l.amount
	}
	if sumBase != 0 {
		t.Errorf("entry does not balance in base (GBP): Σ = %d", sumBase)
	}
	if sumTxn != 0 { // for a single-currency invoice the txn side also nets to zero
		t.Errorf("EUR txn side should also net to zero here: Σ = %d", sumTxn)
	}
}

func TestInvoiceReopenRemovesEntry(t *testing.T) {
	ts := newTestServer(t)
	authHeader := bearer(t, ts, devUserID, devOrgID)
	invID := issueInvoice(t, ts, "GBP", "")

	if got := glLinesForSource(t, ts, "INVOICE", invID); len(got) == 0 {
		t.Fatal("expected a posted entry after issue")
	}
	if rec := statusInvoiceReq(t, ts, invID, authHeader, "reopen"); rec.Code != http.StatusOK {
		t.Fatalf("reopen: status %d body %s", rec.Code, rec.Body.String())
	}
	if got := glLinesForSource(t, ts, "INVOICE", invID); len(got) != 0 {
		t.Errorf("expected the entry removed after reopen, got %d lines", len(got))
	}
}

// TestGLBalanceTriggerRejectsUnbalanced confirms the DB constraint trigger fires at
// commit on an entry whose base amounts don't sum to zero.
func TestGLBalanceTriggerRejectsUnbalanced(t *testing.T) {
	ts := newTestServer(t)
	ctx := context.Background()

	// A real account to point the line at.
	var acctID string
	if err := ts.pool.QueryRow(ctx,
		`SELECT id::text FROM categories WHERE organisation_id = $1 AND nominal_code = '681'`, devOrgID).Scan(&acctID); err != nil {
		t.Fatalf("lookup account: %v", err)
	}

	tx, err := ts.pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer tx.Rollback(ctx)

	var entryID string
	if err := tx.QueryRow(ctx,
		`INSERT INTO gl_journal_entries (organisation_id, entry_date, base_currency, source_type, source_id)
		 VALUES ($1, now()::date, 'GBP', 'MANUAL', NULL) RETURNING id::text`, devOrgID).Scan(&entryID); err != nil {
		t.Fatalf("insert entry: %v", err)
	}
	// A single, deliberately unbalanced line (base 100, not 0).
	if _, err := tx.Exec(ctx,
		`INSERT INTO gl_journal_lines (journal_entry_id, organisation_id, account_id, currency, amount_minor, base_amount_minor)
		 VALUES ($1, $2, $3, 'GBP', 100, 100)`, entryID, devOrgID, acctID); err != nil {
		t.Fatalf("insert line: %v", err)
	}

	// The deferred constraint trigger should reject this at COMMIT.
	if err := tx.Commit(ctx); err == nil {
		t.Error("expected the balance trigger to reject an unbalanced entry at commit, got nil")
	}
}
