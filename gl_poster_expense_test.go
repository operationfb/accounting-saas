package main

// gl_poster_expense_test.go
// =============================================================================
// End-to-end test of the GL poster through the EXPENSE_APPROVED event (real Postgres):
// approving an expense posts a balanced journal entry (Dr expense category NET + Dr
// input VAT 818 / Cr the claimant's user account 907-x GROSS), multi-currency, with the
// reverse-charge variant self-accounting notional VAT (Dr 818 input / Cr 819 output, the
// claimant owed NET). Drives the assembled expenses service via its HTTP handlers,
// against the seeded dev org (which has a chart). Reuses the same-package GL helpers from
// gl_poster_invoice_test.go (glLinesForSource / assertLine / purgeGLEntries).
// =============================================================================

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/operationfb/accounting-saas/internal/expenses"
)

// transitionExpense drives one approval-workflow action and asserts it succeeded (200).
func transitionExpense(t *testing.T, ts *testServer, id, authHeader, action string) {
	t.Helper()
	if rec := postStatus(t, ts, id, authHeader, expenses.ChangeExpenseStatusRequest{Action: action}); rec.Code != http.StatusOK {
		t.Fatalf("%s expense: status %d body %s", action, rec.Code, rec.Body.String())
	}
}

// categoryNominal returns a category's nominal_code — the expense's category leg posts to it.
func categoryNominal(t *testing.T, ts *testServer, categoryID string) string {
	t.Helper()
	var n string
	if err := ts.pool.QueryRow(context.Background(), `SELECT nominal_code FROM categories WHERE id = $1`, categoryID).Scan(&n); err != nil {
		t.Fatalf("category nominal: %v", err)
	}
	return n
}

// glLineByPrefix returns the single journal line whose account nominal starts with prefix
// (used for the claimant's lazily-created 907-N user sub-account, whose suffix depends on
// creation order).
func glLineByPrefix(t *testing.T, lines map[string]glLine, prefix string) glLine {
	t.Helper()
	for nominal, l := range lines {
		if strings.HasPrefix(nominal, prefix) {
			return l
		}
	}
	t.Fatalf("no journal line on a %s* account; got %v", prefix, lines)
	return glLine{}
}

// assertGLBalanced asserts the entry balances in the base currency (Σ base = 0).
func assertGLBalanced(t *testing.T, lines map[string]glLine) {
	t.Helper()
	var sum int64
	for _, l := range lines {
		sum += l.base
	}
	if sum != 0 {
		t.Errorf("entry does not balance in base: Σ = %d", sum)
	}
}

// approveDevExpense creates a GBP expense in the dev org (optionally VAT-rated), submits
// and approves it (posting the GL entry), and returns its id. Registers GL cleanup (the
// append-only guard blocks a plain DELETE, so we go through purgeGLEntries).
func approveDevExpense(t *testing.T, ts *testServer, catID, gross, vatRateID string) string {
	t.Helper()
	authHeader := bearer(t, ts, devUserID, devOrgID)
	id := createExpenseWith(t, ts, devUserID, devOrgID, catID, today(), "GL expense", gross, vatRateID)
	transitionExpense(t, ts, id, authHeader, "submit")
	transitionExpense(t, ts, id, authHeader, "approve")
	t.Cleanup(func() {
		purgeGLEntries(context.Background(), t, ts.pool,
			`organisation_id = $1 AND source_type = 'EXPENSE' AND source_id = $2`, devOrgID, id)
	})
	return id
}

func TestExpenseApprovedPostsBalancedEntry_GBP(t *testing.T) {
	ts := newTestServer(t)
	catID := spendingCategoryForOrg(t, ts, devOrgID)
	vatID := gbStandardVatRateID(t, ts)
	if vatID == "" {
		t.Skip("no GB 20% VAT rate seeded")
	}
	id := approveDevExpense(t, ts, catID, "120.00", vatID)

	lines := glLinesForSource(t, ts, "EXPENSE", id)
	if len(lines) != 3 {
		t.Fatalf("expected 3 journal lines, got %d: %v", len(lines), lines)
	}
	// £120 gross incl 20% VAT → net £100, input VAT £20. Dr category +100.00, Dr 818
	// (VAT_RECLAIMED) +20.00, Cr the claimant's 907-x −120.00. GBP: base == amount.
	assertLine(t, lines, categoryNominal(t, ts, catID), "GBP", 10000, 10000)
	assertLine(t, lines, "818", "GBP", 2000, 2000)
	if u := glLineByPrefix(t, lines, "907-"); u.currency != "GBP" || u.amount != -12000 || u.base != -12000 {
		t.Errorf("user-account leg: got {%s %d/%d}, want {GBP -12000/-12000}", u.currency, u.amount, u.base)
	}
	assertGLBalanced(t, lines)
}

func TestExpenseApprovedPostsBalancedEntry_NoVAT(t *testing.T) {
	ts := newTestServer(t)
	catID := spendingCategoryForOrg(t, ts, devOrgID)
	// No VAT rate selected → vat 0, so the VAT leg is dropped: just Dr category / Cr user.
	id := approveDevExpense(t, ts, catID, "120.00", "")

	lines := glLinesForSource(t, ts, "EXPENSE", id)
	if len(lines) != 2 {
		t.Fatalf("expected 2 journal lines (VAT dropped), got %d: %v", len(lines), lines)
	}
	assertLine(t, lines, categoryNominal(t, ts, catID), "GBP", 12000, 12000) // net == gross
	if u := glLineByPrefix(t, lines, "907-"); u.amount != -12000 {
		t.Errorf("user-account leg: got %d, want -12000", u.amount)
	}
	assertGLBalanced(t, lines)
}

func TestExpenseApprovedPostsBalancedEntry_EUR(t *testing.T) {
	ts := newTestServer(t)
	catID := spendingCategoryForOrg(t, ts, devOrgID)
	vatID := gbStandardVatRateID(t, ts)
	if vatID == "" {
		t.Skip("no GB 20% VAT rate seeded")
	}
	authHeader := bearer(t, ts, devUserID, devOrgID)

	// EUR €120 incl 20% VAT at rate 0.86 GBP/EUR. native = round(amount × 0.86).
	rec := postExpense(t, ts, authHeader, expenses.CreateExpenseRequest{
		CategoryID:       catID,
		DatedOn:          today(),
		Description:      "EUR expense",
		CurrencyCode:     "EUR",
		ExchangeRate:     "0.86",
		GrossValuePounds: "120.00",
		VATRateID:        &vatID,
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create EUR expense: status %d body %s", rec.Code, rec.Body.String())
	}
	id := decodeExpense(t, rec).ID
	t.Cleanup(func() {
		_, _ = ts.pool.Exec(context.Background(), `DELETE FROM expenses WHERE id = $1`, id)
		purgeGLEntries(context.Background(), t, ts.pool,
			`organisation_id = $1 AND source_type = 'EXPENSE' AND source_id = $2`, devOrgID, id)
	})
	transitionExpense(t, ts, id, authHeader, "submit")
	transitionExpense(t, ts, id, authHeader, "approve")

	lines := glLinesForSource(t, ts, "EXPENSE", id)
	if len(lines) != 3 {
		t.Fatalf("expected 3 journal lines, got %d: %v", len(lines), lines)
	}
	// Transaction amounts in EUR; base amounts in GBP at 0.86 (mirrors the invoice EUR test).
	assertLine(t, lines, categoryNominal(t, ts, catID), "EUR", 10000, 8600) // net €100 → £86.00
	assertLine(t, lines, "818", "EUR", 2000, 1720)                          // VAT €20 → £17.20
	if u := glLineByPrefix(t, lines, "907-"); u.currency != "EUR" || u.amount != -12000 || u.base != -10320 {
		t.Errorf("user-account leg: got {%s %d/%d}, want {EUR -12000/-10320}", u.currency, u.amount, u.base)
	}
	assertGLBalanced(t, lines)

	// The EUR transaction side also nets to zero (single-currency entry).
	var sumTxn int64
	for _, l := range lines {
		sumTxn += l.amount
	}
	if sumTxn != 0 {
		t.Errorf("EUR txn side should net to zero: Σ = %d", sumTxn)
	}
}

func TestExpenseApprovedReverseCharge(t *testing.T) {
	ts := newTestServer(t)
	catID := spendingCategoryForOrg(t, ts, devOrgID)
	vatID := gbStandardVatRateID(t, ts)
	if vatID == "" {
		t.Skip("no GB 20% VAT rate seeded")
	}
	authHeader := bearer(t, ts, devUserID, devOrgID)

	// ec_status isn't user-settable via the API yet (hardcoded UK_NON_EC), so create a
	// normal 20% VAT expense and flip it to REVERSE_CHARGE before approval — exactly what
	// a future ec_status entry field will produce. Approval reads the updated row.
	id := createExpenseWith(t, ts, devUserID, devOrgID, catID, today(), "reverse charge", "120.00", vatID)
	transitionExpense(t, ts, id, authHeader, "submit")
	if _, err := ts.pool.Exec(context.Background(), `UPDATE expenses SET ec_status = 'REVERSE_CHARGE' WHERE id = $1`, id); err != nil {
		t.Fatalf("set ec_status: %v", err)
	}
	transitionExpense(t, ts, id, authHeader, "approve")
	t.Cleanup(func() {
		purgeGLEntries(context.Background(), t, ts.pool,
			`organisation_id = $1 AND source_type = 'EXPENSE' AND source_id = $2`, devOrgID, id)
	})

	lines := glLinesForSource(t, ts, "EXPENSE", id)
	if len(lines) != 4 {
		t.Fatalf("expected 4 journal lines (reverse charge), got %d: %v", len(lines), lines)
	}
	// Self-accounted VAT: Dr category (net £100), Dr 818 (+£20 notional input), Cr 819
	// (−£20 notional output), Cr the claimant's 907-x (−£100, NET — supplier charged no
	// VAT). The 818/819 pair nets to zero across the two VAT accounts.
	assertLine(t, lines, categoryNominal(t, ts, catID), "GBP", 10000, 10000)
	assertLine(t, lines, "818", "GBP", 2000, 2000)
	assertLine(t, lines, "819", "GBP", -2000, -2000)
	if u := glLineByPrefix(t, lines, "907-"); u.amount != -10000 || u.base != -10000 {
		t.Errorf("user-account leg: got %d/%d, want -10000/-10000 (net, not gross)", u.amount, u.base)
	}
	assertGLBalanced(t, lines)
}

func TestExpenseApprovedSkipsUnprovisionedOrg(t *testing.T) {
	ts := newTestServer(t)
	org, owner := newOrgWithOwner(t, ts)
	catID := spendingCategoryForOrg(t, ts, org) // seeds a lone spending account, NOT a full chart
	authHeader := bearer(t, ts, owner, org)

	id := createExpenseWith(t, ts, owner, org, catID, today(), "no chart", "120.00", "")
	transitionExpense(t, ts, id, authHeader, "submit")
	transitionExpense(t, ts, id, authHeader, "approve") // must still succeed

	// No chart → the poster fails closed (ErrChartNotProvisioned), the approval commits,
	// and zero GL entries are written — proving the feature-flagged, org-scoped rollout.
	var n int
	if err := ts.pool.QueryRow(context.Background(),
		`SELECT count(*) FROM gl_journal_entries WHERE organisation_id = $1 AND source_type = 'EXPENSE' AND source_id = $2`,
		org, id).Scan(&n); err != nil {
		t.Fatalf("count entries: %v", err)
	}
	if n != 0 {
		t.Errorf("an unprovisioned org should post no GL entry, got %d", n)
	}
}
