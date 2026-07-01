package main

// reports_service_test.go
// =============================================================================
// Integration tests for the Trial Balance report (GET /api/v1/reports/trial-balance).
//
// Like the rest of the suite these hit a REAL PostgreSQL database via the shared
// newTestServer harness (server_test.go) and skip cleanly when DATABASE_URL is
// unset. To avoid touching the shared dev org's ledger, each test runs against a
// throwaway org (newOrgWithOwner) into which it seeds its own Chart-of-Accounts
// rows and balanced journal entries via the helpers below.
//
// Coverage: happy path (accounts split into the right Debit/Credit columns with
// the right pound strings); the Trial Balance Check (total_debit == total_credit);
// a zero-net-but-has-lines account still appears; reversal entries ARE included;
// the ?date filter excludes later entries; multi-tenant isolation; and an
// unauthenticated request is rejected.
// =============================================================================

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/google/uuid"

	reports "github.com/operationfb/accounting-saas/internal/reports"
)

// =============================================================================
// TRIAL BALANCE TEST HELPERS
// =============================================================================

// seedCategory inserts a Chart-of-Accounts row for the org and returns its id.
// Registered cleanup deletes it (LIFO, so it runs before newOrgWithOwner's org
// delete — the categories FK to organisations would otherwise block that delete).
func seedCategory(t *testing.T, ts *testServer, orgID, nominalCode, name, accountType string) string {
	t.Helper()
	ctx := context.Background()
	id := uuid.NewString()
	if _, err := ts.pool.Exec(ctx,
		`INSERT INTO categories (id, organisation_id, nominal_code, name, account_type)
		 VALUES ($1, $2, $3, $4, $5)`, id, orgID, nominalCode, name, accountType); err != nil {
		t.Fatalf("seedCategory(%s): %v", nominalCode, err)
	}
	t.Cleanup(func() {
		// Lines reference this category; remove them first, then the row.
		purgeGLLines(ctx, t, ts.pool, `account_id = $1`, id)
		_, _ = ts.pool.Exec(ctx, `DELETE FROM categories WHERE id = $1`, id)
	})
	return id
}

// jline is one leg of a journal entry the test posts: the GL account and the
// signed base amount in pence (DR +, CR -). Single-currency (GBP) so amount and
// base amount are equal.
type jline struct {
	accountID string
	amount    int64
}

// postEntry writes one balanced journal entry (header + its lines) inside a single
// transaction, so the deferred Σ=0 balance trigger sees a complete, balanced entry
// at COMMIT. entryDate is the accounting date; isReversal flags the audit-reversal
// path. The caller must pass legs that sum to zero.
func postEntry(t *testing.T, ts *testServer, orgID string, entryDate time.Time, isReversal bool, lines ...jline) {
	t.Helper()
	ctx := context.Background()

	var sum int64
	for _, l := range lines {
		sum += l.amount
	}
	if sum != 0 {
		t.Fatalf("postEntry: legs do not balance (Σ = %d); fix the test", sum)
	}

	tx, err := ts.pool.Begin(ctx)
	if err != nil {
		t.Fatalf("postEntry: begin: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }() // no-op after a successful Commit

	var entryID string
	if err := tx.QueryRow(ctx,
		`INSERT INTO gl_journal_entries
		   (organisation_id, entry_date, base_currency, source_type, is_reversal)
		 VALUES ($1, $2, 'GBP', 'MANUAL', $3)
		 RETURNING id`, orgID, entryDate, isReversal).Scan(&entryID); err != nil {
		t.Fatalf("postEntry: insert entry: %v", err)
	}
	t.Cleanup(func() {
		// Remove the entry and its lines (the guard bypass disables cascade, so
		// purgeGLEntries clears the lines explicitly). LIFO before the org delete.
		purgeGLEntries(ctx, t, ts.pool, `id = $1`, entryID)
	})

	for _, l := range lines {
		if _, err := tx.Exec(ctx,
			`INSERT INTO gl_journal_lines
			   (journal_entry_id, organisation_id, account_id, currency, amount_minor, base_amount_minor)
			 VALUES ($1, $2, $3, 'GBP', $4, $4)`,
			entryID, orgID, l.accountID, l.amount); err != nil {
			t.Fatalf("postEntry: insert line: %v", err)
		}
	}

	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("postEntry: commit (balance trigger?): %v", err)
	}
}

// getTrialBalance calls GET /api/v1/reports/trial-balance (optional ?date) with
// the given auth header and returns the recorder.
func getTrialBalance(t *testing.T, ts *testServer, authHeader, date string) *httptest.ResponseRecorder {
	t.Helper()
	path := "/api/v1/reports/trial-balance"
	if date != "" {
		path += "?date=" + date
	}
	rec := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodGet, path, nil)
	if authHeader != "" {
		req.Header.Set("Authorization", authHeader)
	}
	ts.server.router.ServeHTTP(rec, req)
	return rec
}

// decodeTrialBalance pulls the { "trial_balance": {...} } envelope out.
func decodeTrialBalance(t *testing.T, body []byte) reports.TrialBalanceResponse {
	t.Helper()
	var resp struct {
		TrialBalance reports.TrialBalanceResponse `json:"trial_balance"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		t.Fatalf("decodeTrialBalance: %v — body: %s", err, string(body))
	}
	return resp.TrialBalance
}

// rowByCode finds the row for a nominal code (fatal if absent).
func rowByCode(t *testing.T, tb reports.TrialBalanceResponse, code string) reports.TrialBalanceRow {
	t.Helper()
	for _, r := range tb.Rows {
		if r.NominalCode == code {
			return r
		}
	}
	t.Fatalf("trial balance has no row for %q — rows: %+v", code, tb.Rows)
	return reports.TrialBalanceRow{}
}

// =============================================================================
// TESTS
// =============================================================================

// TestTrialBalanceHappyPath seeds a balanced invoice-shaped entry plus a pair of
// entries that net an account to zero, and asserts the report splits each account
// into the correct Debit/Credit column, includes the zero-net account, lists rows
// ordered by nominal code, and balances (total_debit == total_credit).
func TestTrialBalanceHappyPath(t *testing.T) {
	ts := newTestServer(t)
	defer ts.pool.Close()

	orgID, ownerID := newOrgWithOwner(t, ts)

	debtors := seedCategory(t, ts, orgID, "681", "Trade Debtors", "CURRENT_ASSET")
	sales := seedCategory(t, ts, orgID, "001", "Sales", "INCOME")
	vat := seedCategory(t, ts, orgID, "817", "VAT", "TAX_LIABILITY")
	suspense := seedCategory(t, ts, orgID, "999", "Suspense", "SYSTEM")

	today := time.Now()
	// An invoice-shaped entry: Debtors DR £2,400 / Sales CR £2,000 / VAT CR £400.
	postEntry(t, ts, orgID, today, false,
		jline{debtors, 240000}, jline{sales, -200000}, jline{vat, -40000})
	// Two entries that cancel on Suspense, so it nets to ZERO but still has lines
	// (must still appear). They also leave Sales net unchanged (+500 then -500).
	postEntry(t, ts, orgID, today, false, jline{suspense, 50000}, jline{sales, -50000})
	postEntry(t, ts, orgID, today, false, jline{sales, 50000}, jline{suspense, -50000})

	rec := getTrialBalance(t, ts, bearer(t, ts, ownerID, orgID), "")
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d — body: %s", rec.Code, rec.Body.String())
	}
	tb := decodeTrialBalance(t, rec.Body.Bytes())

	if tb.Currency != "GBP" {
		t.Errorf("currency: got %q, want GBP", tb.Currency)
	}

	// Debit-side account.
	if r := rowByCode(t, tb, "681"); r.Debit != "2400.00" || r.Credit != "" {
		t.Errorf("681 Trade Debtors: got debit=%q credit=%q, want 2400.00 / \"\"", r.Debit, r.Credit)
	}
	// Credit-side accounts.
	if r := rowByCode(t, tb, "001"); r.Credit != "2000.00" || r.Debit != "" {
		t.Errorf("001 Sales: got debit=%q credit=%q, want \"\" / 2000.00", r.Debit, r.Credit)
	}
	if r := rowByCode(t, tb, "817"); r.Credit != "400.00" || r.Debit != "" {
		t.Errorf("817 VAT: got debit=%q credit=%q, want \"\" / 400.00", r.Debit, r.Credit)
	}
	// Zero-net account that HAS lines: present, "0.00" in the Debit column.
	if r := rowByCode(t, tb, "999"); r.Debit != "0.00" || r.Credit != "" {
		t.Errorf("999 Suspense (zero net): got debit=%q credit=%q, want 0.00 / \"\"", r.Debit, r.Credit)
	}

	// Rows ordered by nominal code: 001, 681, 817, 999.
	wantOrder := []string{"001", "681", "817", "999"}
	if len(tb.Rows) != len(wantOrder) {
		t.Fatalf("row count: got %d, want %d — rows: %+v", len(tb.Rows), len(wantOrder), tb.Rows)
	}
	for i, code := range wantOrder {
		if tb.Rows[i].NominalCode != code {
			t.Errorf("row %d: got %q, want %q", i, tb.Rows[i].NominalCode, code)
		}
	}

	// The Trial Balance Check: debit total equals credit total (the books balance).
	if tb.TotalDebit != "2400.00" || tb.TotalCredit != "2400.00" {
		t.Errorf("totals: got debit=%q credit=%q, want 2400.00 / 2400.00", tb.TotalDebit, tb.TotalCredit)
	}
}

// TestTrialBalanceReversalIncluded confirms reversal entries are reflected in the
// balances (a reversal is a real journal entry; excluding it would unbalance the
// report). The reversal halves Debtors and Sales back down.
func TestTrialBalanceReversalIncluded(t *testing.T) {
	ts := newTestServer(t)
	defer ts.pool.Close()

	orgID, ownerID := newOrgWithOwner(t, ts)
	debtors := seedCategory(t, ts, orgID, "681", "Trade Debtors", "CURRENT_ASSET")
	sales := seedCategory(t, ts, orgID, "001", "Sales", "INCOME")

	today := time.Now()
	// Original: Debtors DR £1,000 / Sales CR £1,000.
	postEntry(t, ts, orgID, today, false, jline{debtors, 100000}, jline{sales, -100000})
	// Reversal: Debtors CR £400 / Sales DR £400 (is_reversal = TRUE).
	postEntry(t, ts, orgID, today, true, jline{debtors, -40000}, jline{sales, 40000})

	rec := getTrialBalance(t, ts, bearer(t, ts, ownerID, orgID), "")
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d — body: %s", rec.Code, rec.Body.String())
	}
	tb := decodeTrialBalance(t, rec.Body.Bytes())

	// Net after the reversal: Debtors DR £600, Sales CR £600.
	if r := rowByCode(t, tb, "681"); r.Debit != "600.00" {
		t.Errorf("681 after reversal: got debit=%q, want 600.00", r.Debit)
	}
	if r := rowByCode(t, tb, "001"); r.Credit != "600.00" {
		t.Errorf("001 after reversal: got credit=%q, want 600.00", r.Credit)
	}
	if tb.TotalDebit != tb.TotalCredit {
		t.Errorf("reversal must keep the books balanced: debit=%q credit=%q", tb.TotalDebit, tb.TotalCredit)
	}
}

// TestTrialBalanceDateFilter confirms ?date excludes entries dated after it.
func TestTrialBalanceDateFilter(t *testing.T) {
	ts := newTestServer(t)
	defer ts.pool.Close()

	orgID, ownerID := newOrgWithOwner(t, ts)
	debtors := seedCategory(t, ts, orgID, "681", "Trade Debtors", "CURRENT_ASSET")
	sales := seedCategory(t, ts, orgID, "001", "Sales", "INCOME")

	yesterday := time.Now().AddDate(0, 0, -1)
	tomorrow := time.Now().AddDate(0, 0, 1)
	// One entry yesterday, one tomorrow.
	postEntry(t, ts, orgID, yesterday, false, jline{debtors, 100000}, jline{sales, -100000})
	postEntry(t, ts, orgID, tomorrow, false, jline{debtors, 500000}, jline{sales, -500000})

	// As of today: only yesterday's entry counts (Debtors £1,000).
	rec := getTrialBalance(t, ts, bearer(t, ts, ownerID, orgID), time.Now().Format("2006-01-02"))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d — body: %s", rec.Code, rec.Body.String())
	}
	tb := decodeTrialBalance(t, rec.Body.Bytes())
	if r := rowByCode(t, tb, "681"); r.Debit != "1000.00" {
		t.Errorf("681 as of today: got debit=%q, want 1000.00 (tomorrow's entry must be excluded)", r.Debit)
	}
}

// TestTrialBalanceMultiTenant confirms one org's ledger never leaks into another's
// trial balance.
func TestTrialBalanceMultiTenant(t *testing.T) {
	ts := newTestServer(t)
	defer ts.pool.Close()

	orgA, ownerA := newOrgWithOwner(t, ts)
	orgB, _ := newOrgWithOwner(t, ts)

	debtorsA := seedCategory(t, ts, orgA, "681", "Trade Debtors", "CURRENT_ASSET")
	salesA := seedCategory(t, ts, orgA, "001", "Sales", "INCOME")
	debtorsB := seedCategory(t, ts, orgB, "681", "Trade Debtors", "CURRENT_ASSET")
	salesB := seedCategory(t, ts, orgB, "001", "Sales", "INCOME")

	today := time.Now()
	postEntry(t, ts, orgA, today, false, jline{debtorsA, 100000}, jline{salesA, -100000})
	postEntry(t, ts, orgB, today, false, jline{debtorsB, 700000}, jline{salesB, -700000})

	// Org A's owner sees only org A's £1,000, never org B's £7,000.
	rec := getTrialBalance(t, ts, bearer(t, ts, ownerA, orgA), "")
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d — body: %s", rec.Code, rec.Body.String())
	}
	tb := decodeTrialBalance(t, rec.Body.Bytes())
	if r := rowByCode(t, tb, "681"); r.Debit != "1000.00" {
		t.Errorf("org A 681: got debit=%q, want 1000.00", r.Debit)
	}
	if tb.TotalDebit != "1000.00" {
		t.Errorf("org A total_debit: got %q, want 1000.00 (org B's ledger must not leak)", tb.TotalDebit)
	}
}

// TestTrialBalanceRequiresAuth confirms an unauthenticated request is rejected.
func TestTrialBalanceRequiresAuth(t *testing.T) {
	ts := newTestServer(t)
	defer ts.pool.Close()

	rec := getTrialBalance(t, ts, "", "")
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("no auth header: got %d, want 401", rec.Code)
	}
}

// TestTrialBalanceBadDate confirms a malformed ?date is a 400 bad_request.
func TestTrialBalanceBadDate(t *testing.T) {
	ts := newTestServer(t)
	defer ts.pool.Close()

	orgID, ownerID := newOrgWithOwner(t, ts)
	rec := getTrialBalance(t, ts, bearer(t, ts, ownerID, orgID), "29-06-2026")
	if rec.Code != http.StatusBadRequest {
		t.Errorf("bad date: got %d, want 400 — body: %s", rec.Code, rec.Body.String())
	}
}

// =============================================================================
// ACCOUNT TRANSACTIONS TEST HELPERS + TESTS
// =============================================================================

// postSourcedEntry writes one balanced journal entry tagged with a source document
// (source_type + optional source_id) and a narrative, inside a single transaction
// (so the deferred Σ=0 trigger sees a complete entry at COMMIT). sourceID "" → NULL
// (the MANUAL/ad-hoc case). The caller must pass legs that sum to zero.
func postSourcedEntry(t *testing.T, ts *testServer, orgID string, entryDate time.Time, sourceType, sourceID, narrative string, lines ...jline) {
	t.Helper()
	ctx := context.Background()

	var sum int64
	for _, l := range lines {
		sum += l.amount
	}
	if sum != 0 {
		t.Fatalf("postSourcedEntry: legs do not balance (Σ = %d); fix the test", sum)
	}

	tx, err := ts.pool.Begin(ctx)
	if err != nil {
		t.Fatalf("postSourcedEntry: begin: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var src any
	if sourceID != "" {
		src = sourceID
	}
	var entryID string
	if err := tx.QueryRow(ctx,
		`INSERT INTO gl_journal_entries
		   (organisation_id, entry_date, base_currency, source_type, source_id, narrative)
		 VALUES ($1, $2, 'GBP', $3, $4, $5)
		 RETURNING id`, orgID, entryDate, sourceType, src, narrative).Scan(&entryID); err != nil {
		t.Fatalf("postSourcedEntry: insert entry: %v", err)
	}
	t.Cleanup(func() {
		purgeGLEntries(ctx, t, ts.pool, `id = $1`, entryID)
	})

	for _, l := range lines {
		if _, err := tx.Exec(ctx,
			`INSERT INTO gl_journal_lines
			   (journal_entry_id, organisation_id, account_id, currency, amount_minor, base_amount_minor)
			 VALUES ($1, $2, $3, 'GBP', $4, $4)`,
			entryID, orgID, l.accountID, l.amount); err != nil {
			t.Fatalf("postSourcedEntry: insert line: %v", err)
		}
	}

	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("postSourcedEntry: commit (balance trigger?): %v", err)
	}
}

// getAccountTransactions calls GET /api/v1/reports/account-transactions with the
// given query params (empty values are omitted) and returns the recorder.
func getAccountTransactions(t *testing.T, ts *testServer, authHeader, account, from, to string) *httptest.ResponseRecorder {
	t.Helper()
	q := url.Values{}
	if account != "" {
		q.Set("account", account)
	}
	if from != "" {
		q.Set("from", from)
	}
	if to != "" {
		q.Set("to", to)
	}
	path := "/api/v1/reports/account-transactions"
	if enc := q.Encode(); enc != "" {
		path += "?" + enc
	}
	rec := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodGet, path, nil)
	if authHeader != "" {
		req.Header.Set("Authorization", authHeader)
	}
	ts.server.router.ServeHTTP(rec, req)
	return rec
}

// decodeAccountTransactions pulls the { "account_transactions": {...} } envelope out.
func decodeAccountTransactions(t *testing.T, body []byte) reports.AccountTransactionsResponse {
	t.Helper()
	var resp struct {
		AccountTransactions reports.AccountTransactionsResponse `json:"account_transactions"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		t.Fatalf("decodeAccountTransactions: %v — body: %s", err, string(body))
	}
	return resp.AccountTransactions
}

// TestAccountTransactionsHappyPath seeds three invoice-sourced entries hitting the
// Sales account on different dates and asserts the report returns them in date order
// with the right Credit amounts, narratives as Description, source linkage, and a
// Total that sums the credit column (reconciling with the account's balance).
func TestAccountTransactionsHappyPath(t *testing.T) {
	ts := newTestServer(t)
	defer ts.pool.Close()

	orgID, ownerID := newOrgWithOwner(t, ts)
	debtors := seedCategory(t, ts, orgID, "681", "Trade Debtors", "CURRENT_ASSET")
	sales := seedCategory(t, ts, orgID, "001", "Sales", "INCOME")

	inv1 := uuid.NewString()
	inv2 := uuid.NewString()
	// Two invoices: Sales CR £2,000 (21 May) and £3,000 (22 Jun). Debtors is the
	// contra leg each time.
	postSourcedEntry(t, ts, orgID, time.Date(2026, 5, 21, 0, 0, 0, 0, time.UTC), "INVOICE", inv1, "Invoice 001",
		jline{debtors, 200000}, jline{sales, -200000})
	postSourcedEntry(t, ts, orgID, time.Date(2026, 6, 22, 0, 0, 0, 0, time.UTC), "INVOICE", inv2, "Invoice 002",
		jline{debtors, 300000}, jline{sales, -300000})

	rec := getAccountTransactions(t, ts, bearer(t, ts, ownerID, orgID), "001", "", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d — body: %s", rec.Code, rec.Body.String())
	}
	at := decodeAccountTransactions(t, rec.Body.Bytes())

	if at.NominalCode != "001" || at.Name != "Sales" || at.AccountType != "INCOME" {
		t.Errorf("header: got %q/%q/%q, want 001/Sales/INCOME", at.NominalCode, at.Name, at.AccountType)
	}
	if at.Currency != "GBP" {
		t.Errorf("currency: got %q, want GBP", at.Currency)
	}
	if len(at.Rows) != 2 {
		t.Fatalf("row count: got %d, want 2 — rows: %+v", len(at.Rows), at.Rows)
	}
	// Ordered by date: Invoice 001 (21 May) then Invoice 002 (22 Jun).
	if at.Rows[0].Date != "2026-05-21" || at.Rows[0].Description != "Invoice 001" {
		t.Errorf("row 0: got %q/%q, want 2026-05-21/Invoice 001", at.Rows[0].Date, at.Rows[0].Description)
	}
	if at.Rows[0].Credit != "2000.00" || at.Rows[0].Debit != "" {
		t.Errorf("row 0 amounts: got debit=%q credit=%q, want \"\"/2000.00", at.Rows[0].Debit, at.Rows[0].Credit)
	}
	if at.Rows[0].SourceType != "INVOICE" || at.Rows[0].SourceID != inv1 {
		t.Errorf("row 0 source: got %q/%q, want INVOICE/%s", at.Rows[0].SourceType, at.Rows[0].SourceID, inv1)
	}
	if at.Rows[1].Description != "Invoice 002" || at.Rows[1].Credit != "3000.00" {
		t.Errorf("row 1: got %q credit=%q, want Invoice 002 / 3000.00", at.Rows[1].Description, at.Rows[1].Credit)
	}
	// Total credit reconciles with the account balance (£5,000); no debits.
	if at.TotalCredit != "5000.00" || at.TotalDebit != "0.00" {
		t.Errorf("totals: got debit=%q credit=%q, want 0.00 / 5000.00", at.TotalDebit, at.TotalCredit)
	}
}

// TestAccountTransactionsDateFilter confirms from/to bounds exclude out-of-range entries.
func TestAccountTransactionsDateFilter(t *testing.T) {
	ts := newTestServer(t)
	defer ts.pool.Close()

	orgID, ownerID := newOrgWithOwner(t, ts)
	debtors := seedCategory(t, ts, orgID, "681", "Trade Debtors", "CURRENT_ASSET")
	sales := seedCategory(t, ts, orgID, "001", "Sales", "INCOME")

	postSourcedEntry(t, ts, orgID, time.Date(2026, 1, 10, 0, 0, 0, 0, time.UTC), "INVOICE", uuid.NewString(), "Jan invoice",
		jline{debtors, 100000}, jline{sales, -100000})
	postSourcedEntry(t, ts, orgID, time.Date(2026, 6, 10, 0, 0, 0, 0, time.UTC), "INVOICE", uuid.NewString(), "Jun invoice",
		jline{debtors, 500000}, jline{sales, -500000})

	// Window May–Jul: only the June invoice falls in range.
	rec := getAccountTransactions(t, ts, bearer(t, ts, ownerID, orgID), "001", "2026-05-01", "2026-07-31")
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d — body: %s", rec.Code, rec.Body.String())
	}
	at := decodeAccountTransactions(t, rec.Body.Bytes())
	if len(at.Rows) != 1 || at.Rows[0].Description != "Jun invoice" {
		t.Fatalf("date filter: got %d rows %+v, want only the June invoice", len(at.Rows), at.Rows)
	}
	if at.FromDate != "2026-05-01" || at.ToDate != "2026-07-31" {
		t.Errorf("range echo: got %q–%q, want 2026-05-01–2026-07-31", at.FromDate, at.ToDate)
	}
}

// TestAccountTransactionsUnknownAccount confirms a bad/inactive nominal code is 404.
func TestAccountTransactionsUnknownAccount(t *testing.T) {
	ts := newTestServer(t)
	defer ts.pool.Close()

	orgID, ownerID := newOrgWithOwner(t, ts)
	rec := getAccountTransactions(t, ts, bearer(t, ts, ownerID, orgID), "999", "", "")
	if rec.Code != http.StatusNotFound {
		t.Errorf("unknown account: got %d, want 404 — body: %s", rec.Code, rec.Body.String())
	}
}

// TestAccountTransactionsMissingAccount confirms the account param is required (400).
func TestAccountTransactionsMissingAccount(t *testing.T) {
	ts := newTestServer(t)
	defer ts.pool.Close()

	orgID, ownerID := newOrgWithOwner(t, ts)
	rec := getAccountTransactions(t, ts, bearer(t, ts, ownerID, orgID), "", "", "")
	if rec.Code != http.StatusBadRequest {
		t.Errorf("missing account: got %d, want 400 — body: %s", rec.Code, rec.Body.String())
	}
}

// TestAccountTransactionsMultiTenant confirms one org's account never returns another
// org's lines (even for the same nominal code).
func TestAccountTransactionsMultiTenant(t *testing.T) {
	ts := newTestServer(t)
	defer ts.pool.Close()

	orgA, ownerA := newOrgWithOwner(t, ts)
	orgB, _ := newOrgWithOwner(t, ts)
	debtorsA := seedCategory(t, ts, orgA, "681", "Trade Debtors", "CURRENT_ASSET")
	salesA := seedCategory(t, ts, orgA, "001", "Sales", "INCOME")
	debtorsB := seedCategory(t, ts, orgB, "681", "Trade Debtors", "CURRENT_ASSET")
	salesB := seedCategory(t, ts, orgB, "001", "Sales", "INCOME")

	today := time.Now()
	postSourcedEntry(t, ts, orgA, today, "INVOICE", uuid.NewString(), "A invoice",
		jline{debtorsA, 100000}, jline{salesA, -100000})
	postSourcedEntry(t, ts, orgB, today, "INVOICE", uuid.NewString(), "B invoice",
		jline{debtorsB, 700000}, jline{salesB, -700000})

	rec := getAccountTransactions(t, ts, bearer(t, ts, ownerA, orgA), "001", "", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d — body: %s", rec.Code, rec.Body.String())
	}
	at := decodeAccountTransactions(t, rec.Body.Bytes())
	if len(at.Rows) != 1 || at.Rows[0].Description != "A invoice" || at.TotalCredit != "1000.00" {
		t.Fatalf("org A 001: got %d rows %+v total %q, want only A invoice / 1000.00", len(at.Rows), at.Rows, at.TotalCredit)
	}
}

// seedGLEntry inserts one balanced journal entry (header + lines) with full control
// over is_reversal / reverses_entry_id, returning the new entry's id. Unlike
// postSourcedEntry it can reproduce what the real ledger poster writes when it
// supersedes: a reversal that POINTS AT its original (reverses_entry_id). Cleanup is
// LIFO, so a reversal seeded after its original is deleted first — satisfying the FK.
// Legs must sum to zero.
func seedGLEntry(t *testing.T, ts *testServer, orgID string, entryDate time.Time, sourceType, sourceID, narrative string, isReversal bool, reversesEntryID string, lines ...jline) string {
	t.Helper()
	ctx := context.Background()

	var sum int64
	for _, l := range lines {
		sum += l.amount
	}
	if sum != 0 {
		t.Fatalf("seedGLEntry: legs do not balance (Σ = %d); fix the test", sum)
	}

	tx, err := ts.pool.Begin(ctx)
	if err != nil {
		t.Fatalf("seedGLEntry: begin: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// "" → SQL NULL for the optional source_id / reverses_entry_id columns.
	var src, rev any
	if sourceID != "" {
		src = sourceID
	}
	if reversesEntryID != "" {
		rev = reversesEntryID
	}

	var entryID string
	if err := tx.QueryRow(ctx,
		`INSERT INTO gl_journal_entries
		   (organisation_id, entry_date, base_currency, source_type, source_id, narrative, is_reversal, reverses_entry_id)
		 VALUES ($1, $2, 'GBP', $3, $4, $5, $6, $7)
		 RETURNING id`, orgID, entryDate, sourceType, src, narrative, isReversal, rev).Scan(&entryID); err != nil {
		t.Fatalf("seedGLEntry: insert entry: %v", err)
	}
	t.Cleanup(func() {
		purgeGLEntries(ctx, t, ts.pool, `id = $1`, entryID)
	})

	for _, l := range lines {
		if _, err := tx.Exec(ctx,
			`INSERT INTO gl_journal_lines
			   (journal_entry_id, organisation_id, account_id, currency, amount_minor, base_amount_minor)
			 VALUES ($1, $2, $3, 'GBP', $4, $4)`,
			entryID, orgID, l.accountID, l.amount); err != nil {
			t.Fatalf("seedGLEntry: insert line: %v", err)
		}
	}

	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("seedGLEntry: commit (balance trigger?): %v", err)
	}
	return entryID
}

// getAccountTransactionsInc is getAccountTransactions with the include_superseded flag
// (the default helper omits it, i.e. hidden).
func getAccountTransactionsInc(t *testing.T, ts *testServer, authHeader, account string, includeSuperseded bool) *httptest.ResponseRecorder {
	t.Helper()
	q := url.Values{}
	q.Set("account", account)
	if includeSuperseded {
		q.Set("include_superseded", "true")
	}
	rec := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodGet, "/api/v1/reports/account-transactions?"+q.Encode(), nil)
	req.Header.Set("Authorization", authHeader)
	ts.server.router.ServeHTTP(rec, req)
	return rec
}

// TestAccountTransactionsHidesSuperseded confirms the report hides superseded activity
// AND its reversal by default, leaving only the EFFECTIVE (live) entries — and that
// ?include_superseded=true reveals the full chain. Two shapes are seeded exactly as the
// poster writes them: a reverse-then-repost (invoice edited £1,000 → £1,500) and an
// invoice reopen (a reversal with NO fresh entry — the item is undone). Because each
// (original + reversal) pair nets to zero, hiding them leaves the account's NET balance
// unchanged; the column totals differ (showing a pair inflates both columns equally).
func TestAccountTransactionsHidesSuperseded(t *testing.T) {
	ts := newTestServer(t)
	defer ts.pool.Close()

	orgID, ownerID := newOrgWithOwner(t, ts)
	debtors := seedCategory(t, ts, orgID, "681", "Trade Debtors", "CURRENT_ASSET")
	sales := seedCategory(t, ts, orgID, "001", "Sales", "INCOME")

	today := time.Now()

	// Reverse-then-repost: original £1,000, its reversal, then the fresh live £1,500.
	inv := uuid.NewString()
	orig := seedGLEntry(t, ts, orgID, today, "INVOICE", inv, "Invoice 001", false, "",
		jline{debtors, 100000}, jline{sales, -100000})
	seedGLEntry(t, ts, orgID, today, "INVOICE", inv, "Superseded (re-posted)", true, orig,
		jline{debtors, -100000}, jline{sales, 100000})
	seedGLEntry(t, ts, orgID, today, "INVOICE", inv, "Invoice 001", false, "",
		jline{debtors, 150000}, jline{sales, -150000})

	// Invoice reopen: an original £900 and its reversal, with NO fresh entry (undone).
	reopened := uuid.NewString()
	roOrig := seedGLEntry(t, ts, orgID, today, "INVOICE", reopened, "Invoice 002", false, "",
		jline{debtors, 90000}, jline{sales, -90000})
	seedGLEntry(t, ts, orgID, today, "INVOICE", reopened, "Reversed (reopened)", true, roOrig,
		jline{debtors, -90000}, jline{sales, 90000})

	authHeader := bearer(t, ts, ownerID, orgID)

	// --- Default: hidden. Only the live £1,500 line survives on Sales. ---
	rec := getAccountTransactionsInc(t, ts, authHeader, "001", false)
	if rec.Code != http.StatusOK {
		t.Fatalf("default: expected 200, got %d — body: %s", rec.Code, rec.Body.String())
	}
	at := decodeAccountTransactions(t, rec.Body.Bytes())
	if len(at.Rows) != 1 {
		t.Fatalf("default rows: got %d, want 1 (live only) — %+v", len(at.Rows), at.Rows)
	}
	if at.Rows[0].Credit != "1500.00" || at.Rows[0].Debit != "" {
		t.Errorf("default live row: got debit=%q credit=%q, want \"\"/1500.00", at.Rows[0].Debit, at.Rows[0].Credit)
	}
	// Column totals reconcile with the account's net balance (£1,500 CR).
	if at.TotalCredit != "1500.00" || at.TotalDebit != "0.00" {
		t.Errorf("default totals: got debit=%q credit=%q, want 0.00/1500.00", at.TotalDebit, at.TotalCredit)
	}

	// --- include_superseded=true: the full chain. 5 Sales lines: original CR1000,
	// reversal DR1000, live CR1500, reopened CR900, its reversal DR900. ---
	recAll := getAccountTransactionsInc(t, ts, authHeader, "001", true)
	if recAll.Code != http.StatusOK {
		t.Fatalf("include_superseded: expected 200, got %d — body: %s", recAll.Code, recAll.Body.String())
	}
	all := decodeAccountTransactions(t, recAll.Body.Bytes())
	if len(all.Rows) != 5 {
		t.Fatalf("include_superseded rows: got %d, want 5 (full chain) — %+v", len(all.Rows), all.Rows)
	}
	// The reversals inflate both columns equally, so the NET (credit − debit = £1,500)
	// is preserved even though the raw column totals grow.
	if all.TotalDebit != "1900.00" || all.TotalCredit != "3400.00" {
		t.Errorf("include_superseded totals: got debit=%q credit=%q, want 1900.00/3400.00", all.TotalDebit, all.TotalCredit)
	}
}

// TestReportAccountsList confirms GET /reports/accounts returns the org's active
// accounts and requires auth.
func TestReportAccountsList(t *testing.T) {
	ts := newTestServer(t)
	defer ts.pool.Close()

	orgID, ownerID := newOrgWithOwner(t, ts)
	seedCategory(t, ts, orgID, "001", "Sales", "INCOME")
	seedCategory(t, ts, orgID, "681", "Trade Debtors", "CURRENT_ASSET")

	// Unauthenticated → 401.
	noAuth := httptest.NewRecorder()
	reqNoAuth, _ := http.NewRequest(http.MethodGet, "/api/v1/reports/accounts", nil)
	ts.server.router.ServeHTTP(noAuth, reqNoAuth)
	if noAuth.Code != http.StatusUnauthorized {
		t.Errorf("no auth: got %d, want 401", noAuth.Code)
	}

	rec := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodGet, "/api/v1/reports/accounts", nil)
	req.Header.Set("Authorization", bearer(t, ts, ownerID, orgID))
	ts.server.router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d — body: %s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Accounts []reports.AccountSummary `json:"accounts"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v — body: %s", err, rec.Body.String())
	}
	// The two seeded accounts must be present (ordered by nominal code).
	var got001, got681 bool
	for _, a := range resp.Accounts {
		if a.NominalCode == "001" && a.Name == "Sales" {
			got001 = true
		}
		if a.NominalCode == "681" && a.AccountType == "CURRENT_ASSET" {
			got681 = true
		}
	}
	if !got001 || !got681 {
		t.Errorf("accounts list missing seeded accounts: %+v", resp.Accounts)
	}
}
