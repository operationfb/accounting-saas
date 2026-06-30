package main

// fxrevaluation_test.go
// =============================================================================
// Integration tests for the unrealised-FX revaluation (Phase 3): retranslating an
// OPEN foreign invoice's receivable to today's rate posts the swing to 391 so the
// Trial Balance reflects today's value, and a settlement crystallises it (explicit
// reversal) leaving the realised gain in 390 — never double-counted.
//
// These reuse the dev-org GL helpers (issueInvoice / seedRate / glAccountBalance /
// glLinesForSource) and drive ts.fxRevalService.RunRevaluation directly. Each test
// cleans up its INVOICE_REVALUATION entries + seeded rate so the shared dev DB stays
// tidy. The invoice is €120 (net €100 + 20% VAT) booked at 0.86 ⇒ native receivable
// 10320p; the foreign due is 12000p.
// =============================================================================

import (
	"context"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	dbcurrencies "github.com/operationfb/accounting-saas/db/currencies"
	dbfxrates "github.com/operationfb/accounting-saas/db/exchange_rates"
	"github.com/operationfb/accounting-saas/internal/banking"
	"github.com/operationfb/accounting-saas/internal/fxrates"
)

// --- chain test doubles -----------------------------------------------------

// emptyRateProvider is a stub fxrates.Provider that returns no rates (so RefreshRates
// upserts nothing — no shared-DB mutation), but is non-nil so the chain still fires.
type emptyRateProvider struct{}

func (emptyRateProvider) FetchRates(ctx context.Context, base string, on time.Time) (map[string]decimal.Decimal, error) {
	return map[string]decimal.Decimal{}, nil
}

// spyRevaluer records the asOf dates RunRevaluation was called with.
type spyRevaluer struct{ calls []time.Time }

func (s *spyRevaluer) RunRevaluation(ctx context.Context, asOf time.Time) error {
	s.calls = append(s.calls, asOf)
	return nil
}

// TestRateRefreshChainsRevaluation is the regression guard for the stale-391 bug: the
// revaluation MUST be chained at the SERVICE level (RefreshRates), so EVERY refresh path
// — the daily endpoint and the startup best-effort fetch — keeps 391 in sync. A refresh
// that doesn't re-run revaluation is exactly what left 391 stale after the rate moved.
func TestRateRefreshChainsRevaluation(t *testing.T) {
	ts := newTestServer(t)
	defer ts.pool.Close()

	spy := &spyRevaluer{}
	svc := fxrates.NewService(dbfxrates.New(ts.pool), dbcurrencies.New(ts.pool), emptyRateProvider{}, "GBP", "ecb")
	svc.SetRevaluer(spy)

	asOf := time.Date(2026, 6, 15, 0, 0, 0, 0, time.UTC)
	if _, err := svc.RefreshRates(context.Background(), asOf); err != nil {
		t.Fatalf("RefreshRates: %v", err)
	}

	if len(spy.calls) != 1 {
		t.Fatalf("revaluer called %d times, want 1 — a rate refresh must chain the revaluation", len(spy.calls))
	}
	if !spy.calls[0].Equal(asOf) {
		t.Errorf("revaluation asOf = %v, want %v (the refresh date)", spy.calls[0], asOf)
	}
}

// revalLines returns the LIVE (non-reversal) INVOICE_REVALUATION entry's lines for an
// invoice, keyed by nominal code (reuses glLinesForSource's filter).
func revalLines(t *testing.T, ts *testServer, invID string) map[string]glLine {
	t.Helper()
	return glLinesForSource(t, ts, "INVOICE_REVALUATION", invID)
}

// revalNetForInvoice sums base_amount_minor on a nominal across ALL of an invoice's
// INVOICE_REVALUATION entries (live + any reversal) — should be 0 once settled.
func revalNetForInvoice(t *testing.T, ts *testServer, invID, nominal string) int64 {
	t.Helper()
	var bal int64
	if err := ts.pool.QueryRow(context.Background(),
		`SELECT COALESCE(SUM(l.base_amount_minor), 0)
		   FROM gl_journal_lines l
		   JOIN gl_journal_entries e ON e.id = l.journal_entry_id
		   JOIN categories c ON c.id = l.account_id
		  WHERE e.organisation_id = $1 AND e.source_type = 'INVOICE_REVALUATION'
		    AND e.source_id = $2 AND c.nominal_code = $3`, devOrgID, invID, nominal).Scan(&bal); err != nil {
		t.Fatalf("reval net %s: %v", nominal, err)
	}
	return bal
}

// reversalCount returns how many REVERSAL revaluation entries exist for an invoice.
func reversalCount(t *testing.T, ts *testServer, invID string) int {
	t.Helper()
	var n int
	if err := ts.pool.QueryRow(context.Background(),
		`SELECT count(*) FROM gl_journal_entries
		  WHERE organisation_id = $1 AND source_type = 'INVOICE_REVALUATION'
		    AND source_id = $2 AND is_reversal`, devOrgID, invID).Scan(&n); err != nil {
		t.Fatalf("reversal count: %v", err)
	}
	return n
}

// cleanupReval removes an invoice's revaluation entries + a seeded rate.
func cleanupReval(t *testing.T, ts *testServer, invID, currency, day string) {
	t.Cleanup(func() {
		bg := context.Background()
		_, _ = ts.pool.Exec(bg, `DELETE FROM gl_journal_entries WHERE organisation_id=$1 AND source_type='INVOICE_REVALUATION' AND source_id=$2`, devOrgID, invID)
		_, _ = ts.pool.Exec(bg, `DELETE FROM exchange_rates WHERE currency=$1 AND rate_date=$2`, currency, day)
	})
}

// TestUnrealisedRevaluation_Gain revalues an open EUR invoice up (EUR strengthened
// 0.86 → 0.90): the receivable (681) rises and the swing posts to 391, balanced.
func TestUnrealisedRevaluation_Gain(t *testing.T) {
	ts := newTestServer(t)
	defer ts.pool.Close()
	ctx := context.Background()

	invID := issueInvoice(t, ts, "EUR", "0.86") // native receivable 10320p, due 12000p EUR
	cleanupReval(t, ts, invID, "EUR", today())
	seedRate(t, ts, "EUR", today(), "0.90") // home-at-today = 12000·0.90 = 10800 ⇒ U = +480

	if err := ts.fxRevalService.RunRevaluation(ctx, time.Now()); err != nil {
		t.Fatalf("RunRevaluation: %v", err)
	}

	// This invoice's revaluation entry — Gain (U>0): DR 681 +480 / CR 391 −480, all home
	// (GBP), balanced. Asserted per-invoice (RunRevaluation is org-wide, so an org-level 681
	// delta would also pick up any other open foreign invoice in the shared dev org).
	lines := revalLines(t, ts, invID)
	if len(lines) != 2 {
		t.Fatalf("expected 2 revaluation lines, got %d: %v", len(lines), lines)
	}
	assertLine(t, lines, "681", "GBP", 480, 480)   // debtor up by the unrealised gain
	assertLine(t, lines, "391", "GBP", -480, -480) // unrealised gain (credit)
}

// TestUnrealisedRevaluation_ReplacesNotDoubles confirms a second run REPLACES the live
// entry (cumulative-supersede) rather than stacking a second one.
func TestUnrealisedRevaluation_ReplacesNotDoubles(t *testing.T) {
	ts := newTestServer(t)
	defer ts.pool.Close()
	ctx := context.Background()

	invID := issueInvoice(t, ts, "EUR", "0.86")
	cleanupReval(t, ts, invID, "EUR", today())

	seedRate(t, ts, "EUR", today(), "0.90")
	if err := ts.fxRevalService.RunRevaluation(ctx, time.Now()); err != nil {
		t.Fatalf("RunRevaluation #1: %v", err)
	}
	// Rate moves again; rerun. U = 12000·0.95 − 10320 = +1080 (not 480+1080).
	seedRate(t, ts, "EUR", today(), "0.95")
	if err := ts.fxRevalService.RunRevaluation(ctx, time.Now()); err != nil {
		t.Fatalf("RunRevaluation #2: %v", err)
	}

	lines := revalLines(t, ts, invID)
	assertLine(t, lines, "391", "GBP", -1080, -1080) // latest U, replaced not doubled
	assertLine(t, lines, "681", "GBP", 1080, 1080)
	if n := reversalCount(t, ts, invID); n != 0 {
		t.Errorf("an open invoice should have no reversal entries, got %d", n)
	}
}

// TestUnrealisedRevaluation_FullSettlementReverses settles the invoice in full and
// asserts the unrealised 391 is crystallised by an EXPLICIT reversal (audit trail),
// nets to zero, and the realised gain lives independently in 390 (no double-count).
func TestUnrealisedRevaluation_FullSettlementReverses(t *testing.T) {
	ts := newTestServer(t)
	defer ts.pool.Close()
	ctx := context.Background()
	userID, orgID := mustUUID(t, devUserID), mustUUID(t, devOrgID)

	invID := issueInvoice(t, ts, "EUR", "0.86")
	cleanupReval(t, ts, invID, "EUR", today())
	seedRate(t, ts, "EUR", today(), "0.90")

	// Accrue the unrealised gain first (391 = −480).
	if err := ts.fxRevalService.RunRevaluation(ctx, time.Now()); err != nil {
		t.Fatalf("RunRevaluation: %v", err)
	}
	if got := revalNetForInvoice(t, ts, invID, "391"); got != -480 {
		t.Fatalf("391 after revaluation = %d, want -480", got)
	}

	// Settle the invoice in full from a EUR bank account at 0.90.
	acc, err := ts.bankingService.CreateBankAccount(ctx, userID, orgID, bankReq("EUR settle", func(r *banking.CreateBankAccountRequest) { r.Currency = "EUR" }))
	if err != nil {
		t.Fatalf("create EUR bank account: %v", err)
	}
	txnID := newBankTxn(t, ts, acc.ID, 12000) // €120.00 in
	t.Cleanup(func() {
		bg := context.Background()
		_, _ = ts.pool.Exec(bg, `DELETE FROM gl_journal_entries WHERE organisation_id=$1 AND source_type IN ('INVOICE_RECEIPT')`, devOrgID)
		_, _ = ts.pool.Exec(bg, `DELETE FROM categories WHERE organisation_id=$1 AND bank_account_id=$2`, devOrgID, acc.ID)
		_, _ = ts.pool.Exec(bg, `DELETE FROM bank_transaction_explanations WHERE bank_transaction_id=$1`, txnID)
		_, _ = ts.pool.Exec(bg, `DELETE FROM bank_transactions WHERE bank_account_id=$1`, acc.ID)
		cleanupBankAccount(t, ts, acc.ID)
	})

	if _, err := ts.bankingService.CreateExplanation(ctx, userID, orgID, acc.ID, txnID, banking.CreateExplanationRequest{
		Type: "INVOICE_RECEIPT", Amount: "120.00", PaidInvoiceID: &invID,
	}); err != nil {
		t.Fatalf("explain full receipt: %v", err)
	}

	// Crystallised: an explicit reversal exists, and 391's net for this invoice is zero.
	if n := reversalCount(t, ts, invID); n != 1 {
		t.Errorf("expected exactly 1 reversal entry on full settlement, got %d", n)
	}
	if got := revalNetForInvoice(t, ts, invID, "391"); got != 0 {
		t.Errorf("391 net after settlement = %d, want 0 (crystallised)", got)
	}
	if got := revalNetForInvoice(t, ts, invID, "681"); got != 0 {
		t.Errorf("681 revaluation net after settlement = %d, want 0", got)
	}

	// Realised gain lives in 390 via the receipt: €120 at 0.90 (10800) − booking relief
	// (10320) = +480 ⇒ CR 390 (−480 signed). Independent of the reversed 391.
	receipt := glLinesForSource(t, ts, "INVOICE_RECEIPT", liveReceiptExplID(t, ts))
	assertLine(t, receipt, "390", "GBP", -480, -480)
}
