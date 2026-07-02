package main

// banking_service_test.go
// =============================================================================
// Service-level integration tests for the banking domain (internal/banking),
// driven through the real harness (newTestServer wires ts.bankingService) against
// real PostgreSQL — the same approach integration_service_test.go uses for the
// internal/integrations service. The data-layer query tests live in banking_test.go;
// these cover the SERVICE concerns: money conversion at the boundary, the derived
// balance, the transactional primary-account flip, authorisation, and validation.
//
// Each test uses a FRESH ephemeral org (newOrgWithOwner) and hard-deletes its bank
// rows in cleanup (so the ephemeral org's own cleanup can drop the org without a
// FK violation, and the shared dev DB stays clean).
// =============================================================================

import (
	"context"
	"strings"
	"testing"

	"github.com/google/uuid"

	banking "github.com/operationfb/accounting-saas/internal/banking"
	kernel "github.com/operationfb/accounting-saas/internal/kernel"
)

// bankReq builds a minimal valid create request (override via mutate).
func bankReq(name string, mutate func(*banking.CreateBankAccountRequest)) banking.CreateBankAccountRequest {
	req := banking.CreateBankAccountRequest{Name: name, Currency: "GBP", OpeningBalance: "0.00"}
	if mutate != nil {
		mutate(&req)
	}
	return req
}

// cleanupBankAccount hard-deletes an account + its transactions after the test.
// Registered AFTER newOrgWithOwner, so (LIFO) it runs first — before the org row
// it points at is removed.
func cleanupBankAccount(t *testing.T, ts *testServer, id string) {
	t.Cleanup(func() {
		ctx := context.Background()
		_, _ = ts.pool.Exec(ctx, `DELETE FROM bank_transactions WHERE bank_account_id = $1`, id)
		_, _ = ts.pool.Exec(ctx, `DELETE FROM bank_accounts WHERE id = $1`, id)
	})
}

// addTxn inserts one transaction straight into the DB (there is no transaction
// service yet) so the derived-balance path can be exercised. Cleaned up with the account.
func addTxn(t *testing.T, ts *testServer, orgID, accountID string, amountMinor int64) {
	t.Helper()
	if _, err := ts.pool.Exec(context.Background(), `
		INSERT INTO bank_transactions (organisation_id, bank_account_id, dated_on, amount_minor, status, source)
		VALUES ($1, $2, CURRENT_DATE, $3, 'unexplained', 'manual')`, orgID, accountID, amountMinor); err != nil {
		t.Fatalf("addTxn: %v", err)
	}
}

// addMember inserts an active member with the given role into an org, with cleanup.
func addMember(t *testing.T, ts *testServer, orgID, role string) string {
	t.Helper()
	ctx := context.Background()
	uid := uuid.NewString()
	if _, err := ts.pool.Exec(ctx,
		`INSERT INTO users (id, email, first_name, last_name, is_active, email_verified_at)
		 VALUES ($1, $2, 'Mem', 'Ber', TRUE, now())`, uid, "mem-"+uid+"@test.local"); err != nil {
		t.Fatalf("insert member user: %v", err)
	}
	if _, err := ts.pool.Exec(ctx,
		`INSERT INTO organisation_memberships (organisation_id, user_id, role, status)
		 VALUES ($1, $2, $3, 'active')`, orgID, uid, role); err != nil {
		t.Fatalf("insert membership: %v", err)
	}
	t.Cleanup(func() {
		_, _ = ts.pool.Exec(ctx, `DELETE FROM organisation_memberships WHERE user_id = $1`, uid)
		_, _ = ts.pool.Exec(ctx, `DELETE FROM users WHERE id = $1`, uid)
	})
	return uid
}

func TestBankAccountService(t *testing.T) {
	ts := newTestServer(t)
	// NOT defer: with parallel subtests this parent function returns BEFORE the
	// paused subtests resume, so a defer would close the pool out from under them.
	// t.Cleanup runs only after the test AND all its subtests finish.
	t.Cleanup(func() { ts.pool.Close() })
	ctx := context.Background()
	svc := ts.bankingService

	t.Run("create round-trips money + fields and applies defaults", func(t *testing.T) {
		t.Parallel() // each subtest gets its own ephemeral org, so they're data-isolated and safe to overlap; the win is hiding the ~95ms-per-query latency to the remote dev DB behind concurrency
		org, user := newOrgWithOwner(t, ts)
		resp, err := svc.CreateBankAccount(ctx, mustUUID(t, user), mustUUID(t, org),
			bankReq("Acme Current", func(r *banking.CreateBankAccountRequest) {
				r.OpeningBalance = "1234.56"
				bn, sc := "NatWest", "601441"
				r.BankName, r.SortCode = &bn, &sc
			}))
		if err != nil {
			t.Fatalf("create: %v", err)
		}
		cleanupBankAccount(t, ts, resp.ID)

		if resp.OpeningBalance != "1234.56" {
			t.Errorf("opening_balance round-trip: got %q, want 1234.56", resp.OpeningBalance)
		}
		if resp.CurrentBalance != "1234.56" { // fresh account: current == opening
			t.Errorf("current_balance (fresh): got %q, want 1234.56", resp.CurrentBalance)
		}
		if resp.Name != "Acme Current" || resp.BankName == nil || *resp.BankName != "NatWest" {
			t.Errorf("field round-trip: name=%q bank=%v", resp.Name, resp.BankName)
		}
		if resp.Currency != "GBP" || resp.Status != "active" {
			t.Errorf("defaults: currency=%q status=%q, want GBP/active", resp.Currency, resp.Status)
		}
		if !resp.ShowOnInvoices || !resp.GuessExplanations {
			t.Error("show_on_invoices and guess_explanations should default to true when omitted")
		}
	})

	t.Run("derived current_balance reflects transactions", func(t *testing.T) {
		t.Parallel()
		org, user := newOrgWithOwner(t, ts)
		resp, err := svc.CreateBankAccount(ctx, mustUUID(t, user), mustUUID(t, org),
			bankReq("Balance", func(r *banking.CreateBankAccountRequest) { r.OpeningBalance = "1000.00" }))
		if err != nil {
			t.Fatalf("create: %v", err)
		}
		cleanupBankAccount(t, ts, resp.ID)
		addTxn(t, ts, org, resp.ID, 50_000)  // +£500.00
		addTxn(t, ts, org, resp.ID, -20_000) // -£200.00

		got, err := svc.GetBankAccount(ctx, mustUUID(t, user), mustUUID(t, org), resp.ID)
		if err != nil {
			t.Fatalf("get: %v", err)
		}
		if got.CurrentBalance != "1300.00" { // 1000 + 500 - 200
			t.Errorf("derived current_balance: got %q, want 1300.00", got.CurrentBalance)
		}
		list, err := svc.ListBankAccounts(ctx, mustUUID(t, user), mustUUID(t, org))
		if err != nil || len(list) != 1 {
			t.Fatalf("list: %d accounts (err %v), want 1", len(list), err)
		}
		if list[0].CurrentBalance != "1300.00" {
			t.Errorf("list current_balance: got %q, want 1300.00", list[0].CurrentBalance)
		}
	})

	t.Run("creating a primary unsets the previous primary (transactional)", func(t *testing.T) {
		t.Parallel()
		org, user := newOrgWithOwner(t, ts)
		primary := func(r *banking.CreateBankAccountRequest) { r.IsPrimary = true }
		a, err := svc.CreateBankAccount(ctx, mustUUID(t, user), mustUUID(t, org), bankReq("First", primary))
		if err != nil {
			t.Fatalf("create A: %v", err)
		}
		cleanupBankAccount(t, ts, a.ID)
		// Without the in-tx unset, this second primary would violate the unique index.
		b, err := svc.CreateBankAccount(ctx, mustUUID(t, user), mustUUID(t, org), bankReq("Second", primary))
		if err != nil {
			t.Fatalf("create B (should unset A, not error): %v", err)
		}
		cleanupBankAccount(t, ts, b.ID)

		ga, _ := svc.GetBankAccount(ctx, mustUUID(t, user), mustUUID(t, org), a.ID)
		gb, _ := svc.GetBankAccount(ctx, mustUUID(t, user), mustUUID(t, org), b.ID)
		if ga.IsPrimary {
			t.Error("A should no longer be primary after B claimed it")
		}
		if !gb.IsPrimary {
			t.Error("B should be the primary account")
		}
	})

	t.Run("update edits fields and can move the primary", func(t *testing.T) {
		t.Parallel()
		org, user := newOrgWithOwner(t, ts)
		a, err := svc.CreateBankAccount(ctx, mustUUID(t, user), mustUUID(t, org),
			bankReq("A", func(r *banking.CreateBankAccountRequest) { r.IsPrimary = true }))
		if err != nil {
			t.Fatalf("create A: %v", err)
		}
		cleanupBankAccount(t, ts, a.ID)
		b, err := svc.CreateBankAccount(ctx, mustUUID(t, user), mustUUID(t, org), bankReq("B", nil))
		if err != nil {
			t.Fatalf("create B: %v", err)
		}
		cleanupBankAccount(t, ts, b.ID)

		ub, err := svc.UpdateBankAccount(ctx, mustUUID(t, user), mustUUID(t, org), b.ID,
			bankReq("B renamed", func(r *banking.CreateBankAccountRequest) { r.IsPrimary = true }))
		if err != nil {
			t.Fatalf("update B: %v", err)
		}
		if ub.Name != "B renamed" || !ub.IsPrimary {
			t.Errorf("update result: name=%q primary=%v", ub.Name, ub.IsPrimary)
		}
		if ga, _ := svc.GetBankAccount(ctx, mustUUID(t, user), mustUUID(t, org), a.ID); ga.IsPrimary {
			t.Error("A should have been unset when B became primary via update")
		}
	})

	t.Run("soft-delete removes the account from get and list", func(t *testing.T) {
		t.Parallel()
		org, user := newOrgWithOwner(t, ts)
		a, err := svc.CreateBankAccount(ctx, mustUUID(t, user), mustUUID(t, org), bankReq("Gone", nil))
		if err != nil {
			t.Fatalf("create: %v", err)
		}
		cleanupBankAccount(t, ts, a.ID)
		if err := svc.DeleteBankAccount(ctx, mustUUID(t, user), mustUUID(t, org), a.ID); err != nil {
			t.Fatalf("delete: %v", err)
		}
		if _, err := svc.GetBankAccount(ctx, mustUUID(t, user), mustUUID(t, org), a.ID); err == nil {
			t.Error("expected an error getting a soft-deleted account")
		} else {
			assertAppCode(t, err, kernel.ErrCodeNotFound)
		}
		if list, _ := svc.ListBankAccounts(ctx, mustUUID(t, user), mustUUID(t, org)); len(list) != 0 {
			t.Errorf("list after delete: want 0, got %d", len(list))
		}
	})

	t.Run("a non-admin member cannot create (403)", func(t *testing.T) {
		t.Parallel()
		org, _ := newOrgWithOwner(t, ts)
		member := addMember(t, ts, org, "member")
		_, err := svc.CreateBankAccount(ctx, mustUUID(t, member), mustUUID(t, org), bankReq("Nope", nil))
		assertAppCode(t, err, kernel.ErrCodeForbidden)
	})

	t.Run("cannot read another org's account (404)", func(t *testing.T) {
		t.Parallel()
		orgA, userA := newOrgWithOwner(t, ts)
		a, err := svc.CreateBankAccount(ctx, mustUUID(t, userA), mustUUID(t, orgA), bankReq("A", nil))
		if err != nil {
			t.Fatalf("create: %v", err)
		}
		cleanupBankAccount(t, ts, a.ID)
		orgB, userB := newOrgWithOwner(t, ts)
		// userB asks, scoped to their own org, for org A's id → org-scoped query misses → 404.
		_, err = svc.GetBankAccount(ctx, mustUUID(t, userB), mustUUID(t, orgB), a.ID)
		assertAppCode(t, err, kernel.ErrCodeNotFound)
	})

	t.Run("an invalid opening_balance is a 422", func(t *testing.T) {
		t.Parallel()
		org, user := newOrgWithOwner(t, ts)
		_, err := svc.CreateBankAccount(ctx, mustUUID(t, user), mustUUID(t, org),
			bankReq("Bad", func(r *banking.CreateBankAccountRequest) { r.OpeningBalance = "not-a-number" }))
		assertAppCode(t, err, kernel.ErrCodeValidation)
	})

	t.Run("ListTransactions returns the statement with a running balance", func(t *testing.T) {
		t.Parallel()
		org, user := newOrgWithOwner(t, ts)
		acc, err := svc.CreateBankAccount(ctx, mustUUID(t, user), mustUUID(t, org),
			bankReq("Statement", func(r *banking.CreateBankAccountRequest) { r.OpeningBalance = "1000.00" }))
		if err != nil {
			t.Fatalf("create: %v", err)
		}
		cleanupBankAccount(t, ts, acc.ID)
		// Distinct dates so the chronological (ASC) order is deterministic.
		mkTxn := func(date string, amount int64) {
			if _, err := ts.pool.Exec(ctx,
				`INSERT INTO bank_transactions (organisation_id, bank_account_id, dated_on, amount_minor, status, source)
				 VALUES ($1, $2, $3, $4, 'unexplained', 'manual')`, org, acc.ID, date, amount); err != nil {
				t.Fatalf("insert txn: %v", err)
			}
		}
		mkTxn("2026-06-01", 50_000)  // +£500.00 in
		mkTxn("2026-06-02", -20_000) // -£200.00 out

		st, err := svc.ListTransactions(ctx, mustUUID(t, user), mustUUID(t, org), acc.ID)
		if err != nil {
			t.Fatalf("ListTransactions: %v", err)
		}
		if len(st.Transactions) != 2 {
			t.Fatalf("transactions: got %d, want 2", len(st.Transactions))
		}
		in, out := st.Transactions[0], st.Transactions[1] // oldest first

		// Money split by sign: exactly one of in/out is set per row.
		if in.MoneyIn == nil || *in.MoneyIn != "500.00" || in.MoneyOut != nil {
			t.Errorf("money-in row: in=%v out=%v, want in=500.00 / out=nil", in.MoneyIn, in.MoneyOut)
		}
		if out.MoneyOut == nil || *out.MoneyOut != "200.00" || out.MoneyIn != nil {
			t.Errorf("money-out row: in=%v out=%v, want out=200.00 / in=nil", out.MoneyIn, out.MoneyOut)
		}
		// Running balance accumulates: 1000 +500 = 1500, then -200 = 1300.
		if in.RunningBalance != "1500.00" {
			t.Errorf("running balance after +500: got %q, want 1500.00", in.RunningBalance)
		}
		if out.RunningBalance != "1300.00" {
			t.Errorf("running balance after -200: got %q, want 1300.00", out.RunningBalance)
		}
		// The final running balance equals the account's derived current balance.
		if out.RunningBalance != st.Account.CurrentBalance {
			t.Errorf("final running balance %q != account.current_balance %q", out.RunningBalance, st.Account.CurrentBalance)
		}

		// Authz: a non-member of the org → 403; another org's id → 404.
		otherOrg, otherUser := newOrgWithOwner(t, ts)
		if _, err := svc.ListTransactions(ctx, mustUUID(t, otherUser), mustUUID(t, org), acc.ID); err == nil {
			t.Error("expected an error for a non-member caller")
		} else {
			assertAppCode(t, err, kernel.ErrCodeForbidden)
		}
		if _, err := svc.ListTransactions(ctx, mustUUID(t, otherUser), mustUUID(t, otherOrg), acc.ID); err == nil {
			t.Error("expected 404 reading another org's account")
		} else {
			assertAppCode(t, err, kernel.ErrCodeNotFound)
		}
	})

	t.Run("opening balance locks once the account has transactions", func(t *testing.T) {
		t.Parallel()
		org, user := newOrgWithOwner(t, ts)
		acc, err := svc.CreateBankAccount(ctx, mustUUID(t, user), mustUUID(t, org),
			bankReq("Lockable", func(r *banking.CreateBankAccountRequest) { r.OpeningBalance = "100.00" }))
		if err != nil {
			t.Fatalf("create: %v", err)
		}
		cleanupBankAccount(t, ts, acc.ID)

		// No transactions yet → editable, and a change to the opening balance is allowed.
		if !acc.OpeningBalanceEditable {
			t.Error("a fresh account's opening balance should be editable")
		}
		upd, err := svc.UpdateBankAccount(ctx, mustUUID(t, user), mustUUID(t, org), acc.ID,
			bankReq("Lockable", func(r *banking.CreateBankAccountRequest) { r.OpeningBalance = "250.00" }))
		if err != nil {
			t.Fatalf("update opening (no txns): %v", err)
		}
		if upd.OpeningBalance != "250.00" {
			t.Errorf("opening balance change with no txns: got %q, want 250.00", upd.OpeningBalance)
		}

		// Add a transaction → the opening balance locks.
		addTxn(t, ts, org, acc.ID, 5_000)
		locked, err := svc.GetBankAccount(ctx, mustUUID(t, user), mustUUID(t, org), acc.ID)
		if err != nil {
			t.Fatalf("get: %v", err)
		}
		if locked.OpeningBalanceEditable {
			t.Error("opening balance should be locked once the account has transactions")
		}
		// Changing it now is rejected (422)...
		_, err = svc.UpdateBankAccount(ctx, mustUUID(t, user), mustUUID(t, org), acc.ID,
			bankReq("Lockable", func(r *banking.CreateBankAccountRequest) { r.OpeningBalance = "999.00" }))
		assertAppCode(t, err, kernel.ErrCodeValidation)
		// ...but re-sending the SAME opening balance (what the disabled form does) still
		// lets other fields update.
		ok, err := svc.UpdateBankAccount(ctx, mustUUID(t, user), mustUUID(t, org), acc.ID,
			bankReq("Renamed", func(r *banking.CreateBankAccountRequest) { r.OpeningBalance = "250.00" }))
		if err != nil {
			t.Fatalf("update with unchanged opening: %v", err)
		}
		if ok.Name != "Renamed" {
			t.Errorf("name should have updated alongside an unchanged opening balance: got %q", ok.Name)
		}
	})

	t.Run("manual transaction add / edit / delete + new fields", func(t *testing.T) {
		t.Parallel()
		org, user := newOrgWithOwner(t, ts)
		acc, err := svc.CreateBankAccount(ctx, mustUUID(t, user), mustUUID(t, org),
			bankReq("Txns", func(r *banking.CreateBankAccountRequest) { r.OpeningBalance = "1000.00" }))
		if err != nil {
			t.Fatalf("create account: %v", err)
		}
		cleanupBankAccount(t, ts, acc.ID)

		desc := "Stationery"
		st, err := svc.CreateTransaction(ctx, mustUUID(t, user), mustUUID(t, org), acc.ID,
			banking.CreateBankTransactionRequest{DatedOn: "2026-06-10", Description: &desc, Direction: "out", Amount: "30.00"})
		if err != nil {
			t.Fatalf("create txn: %v", err)
		}
		if len(st.Transactions) != 1 {
			t.Fatalf("want 1 transaction, got %d", len(st.Transactions))
		}
		line := st.Transactions[0]
		if line.Source != "manual" || line.Status != "unexplained" || !line.IsManual {
			t.Errorf("manual line: source=%q status=%q is_manual=%v", line.Source, line.Status, line.IsManual)
		}
		if line.TransactionType == nil || *line.TransactionType != "DEBIT" {
			t.Errorf("transaction_type: got %v, want DEBIT (money out)", line.TransactionType)
		}
		if line.MoneyOut == nil || *line.MoneyOut != "30.00" || line.MoneyIn != nil {
			t.Errorf("money split: in=%v out=%v", line.MoneyIn, line.MoneyOut)
		}
		if line.UnexplainedAmount != "-30.00" { // signed like amount (FreeAgent convention); = amount until reconciled
			t.Errorf("unexplained_amount: got %q, want -30.00", line.UnexplainedAmount)
		}
		if line.RunningBalance != "970.00" || st.Account.CurrentBalance != "970.00" { // 1000 - 30
			t.Errorf("balances: running=%q current=%q, want 970.00", line.RunningBalance, st.Account.CurrentBalance)
		}

		// Edit → £45 out; statement + current balance reflect it.
		st, err = svc.UpdateTransaction(ctx, mustUUID(t, user), mustUUID(t, org), acc.ID, line.ID,
			banking.CreateBankTransactionRequest{DatedOn: "2026-06-10", Description: &desc, Direction: "out", Amount: "45.00"})
		if err != nil {
			t.Fatalf("update txn: %v", err)
		}
		if st.Transactions[0].MoneyOut == nil || *st.Transactions[0].MoneyOut != "45.00" || st.Account.CurrentBalance != "955.00" {
			t.Errorf("after edit: out=%v current=%q, want 45.00 / 955.00", st.Transactions[0].MoneyOut, st.Account.CurrentBalance)
		}

		// Delete → gone, balance reverts to the opening.
		st, err = svc.DeleteTransaction(ctx, mustUUID(t, user), mustUUID(t, org), acc.ID, line.ID)
		if err != nil {
			t.Fatalf("delete txn: %v", err)
		}
		if len(st.Transactions) != 0 || st.Account.CurrentBalance != "1000.00" {
			t.Errorf("after delete: %d txns, current=%q, want 0 / 1000.00", len(st.Transactions), st.Account.CurrentBalance)
		}
	})

	t.Run("only manual lines can be edited or deleted (source guard)", func(t *testing.T) {
		t.Parallel()
		org, user := newOrgWithOwner(t, ts)
		acc, err := svc.CreateBankAccount(ctx, mustUUID(t, user), mustUUID(t, org), bankReq("Guard", nil))
		if err != nil {
			t.Fatalf("create account: %v", err)
		}
		cleanupBankAccount(t, ts, acc.ID)
		// A FEED line (the bank's truth) — inserted directly with source='feed'.
		var feedID string
		if err := ts.pool.QueryRow(ctx, `
			INSERT INTO bank_transactions (organisation_id, bank_account_id, dated_on, amount_minor, status, source, transaction_type)
			VALUES ($1, $2, CURRENT_DATE, -5000, 'unexplained', 'feed', 'POS') RETURNING id::text`,
			org, acc.ID).Scan(&feedID); err != nil {
			t.Fatalf("insert feed txn: %v", err)
		}

		req := banking.CreateBankTransactionRequest{DatedOn: "2026-06-10", Direction: "out", Amount: "10.00"}
		if _, err := svc.UpdateTransaction(ctx, mustUUID(t, user), mustUUID(t, org), acc.ID, feedID, req); err == nil {
			t.Error("editing a feed line should be rejected")
		} else {
			assertAppCode(t, err, kernel.ErrCodeValidation)
		}
		if _, err := svc.DeleteTransaction(ctx, mustUUID(t, user), mustUUID(t, org), acc.ID, feedID); err == nil {
			t.Error("deleting a feed line should be rejected")
		} else {
			assertAppCode(t, err, kernel.ErrCodeValidation)
		}

		// The feed line reports is_manual=false and its passed-through transaction_type.
		st, err := svc.ListTransactions(ctx, mustUUID(t, user), mustUUID(t, org), acc.ID)
		if err != nil {
			t.Fatalf("list: %v", err)
		}
		if len(st.Transactions) != 1 || st.Transactions[0].IsManual {
			t.Errorf("a feed line should be is_manual=false")
		}
		if st.Transactions[0].TransactionType == nil || *st.Transactions[0].TransactionType != "POS" {
			t.Errorf("feed transaction_type: got %v, want POS", st.Transactions[0].TransactionType)
		}
	})

	t.Run("manual transaction authz + validation", func(t *testing.T) {
		t.Parallel()
		org, user := newOrgWithOwner(t, ts)
		acc, err := svc.CreateBankAccount(ctx, mustUUID(t, user), mustUUID(t, org), bankReq("AV", nil))
		if err != nil {
			t.Fatalf("create account: %v", err)
		}
		cleanupBankAccount(t, ts, acc.ID)

		good := banking.CreateBankTransactionRequest{DatedOn: "2026-06-10", Direction: "in", Amount: "10.00"}
		// A non-admin member cannot add a transaction.
		member := addMember(t, ts, org, "member")
		if _, err := svc.CreateTransaction(ctx, mustUUID(t, member), mustUUID(t, org), acc.ID, good); err == nil {
			t.Error("a non-admin member should not be able to add a transaction")
		} else {
			assertAppCode(t, err, kernel.ErrCodeForbidden)
		}
		// Validation: amount ≤ 0, bad direction, bad date → 422.
		for _, bad := range []banking.CreateBankTransactionRequest{
			{DatedOn: "2026-06-10", Direction: "in", Amount: "0"},
			{DatedOn: "2026-06-10", Direction: "sideways", Amount: "10.00"},
			{DatedOn: "10/06/2026", Direction: "in", Amount: "10.00"},
		} {
			if _, err := svc.CreateTransaction(ctx, mustUUID(t, user), mustUUID(t, org), acc.ID, bad); err == nil {
				t.Errorf("expected 422 for %+v", bad)
			} else {
				assertAppCode(t, err, kernel.ErrCodeValidation)
			}
		}
	})

	// importCSV uploads with NO mapping (the service auto-detects the format) — covers our
	// own template + the no-mapping endpoint contract. The mapped path has its own helper below.
	importCSV := func(t *testing.T, userID, orgID, accID, csv string) (*banking.StatementImportResponse, error) {
		t.Helper()
		return svc.ImportStatement(ctx, mustUUID(t, userID), mustUUID(t, orgID), accID, strings.NewReader(csv), nil)
	}
	// importOFX is the same entry point — ImportStatement auto-detects CSV vs OFX from
	// the bytes — but named for intent in the OFX subtests below.
	importOFX := func(t *testing.T, userID, orgID, accID, ofx string) (*banking.StatementImportResponse, error) {
		t.Helper()
		return svc.ImportStatement(ctx, mustUUID(t, userID), mustUUID(t, orgID), accID, strings.NewReader(ofx), nil)
	}

	t.Run("statement import: valid CSV creates statement lines + is idempotent", func(t *testing.T) {
		t.Parallel()
		org, user := newOrgWithOwner(t, ts)
		acc, err := svc.CreateBankAccount(ctx, mustUUID(t, user), mustUUID(t, org),
			bankReq("Import", func(r *banking.CreateBankAccountRequest) { r.OpeningBalance = "1000.00" }))
		if err != nil {
			t.Fatalf("create account: %v", err)
		}
		cleanupBankAccount(t, ts, acc.ID)

		csv := "date,description,amount\n" +
			"11/06/2026,Salary,16413.59\n" + // positive = money in
			"12/06/2026,Coffee,-10.08\n" // leading - = money out
		res, err := importCSV(t, user, org, acc.ID, csv)
		if err != nil {
			t.Fatalf("import: %v", err)
		}
		if res.Imported != 2 || res.SkippedDuplicates != 0 || res.Total != 2 {
			t.Errorf("summary: got imported=%d skipped=%d total=%d, want 2/0/2", res.Imported, res.SkippedDuplicates, res.Total)
		}
		if len(res.Transactions) != 2 {
			t.Fatalf("transactions: got %d, want 2", len(res.Transactions))
		}
		salary, coffee := res.Transactions[0], res.Transactions[1] // chronological
		if salary.Source != "statement" || salary.Status != "unexplained" {
			t.Errorf("imported classification: source=%q status=%q, want statement/unexplained", salary.Source, salary.Status)
		}
		if salary.MoneyIn == nil || *salary.MoneyIn != "16413.59" || salary.TransactionType == nil || *salary.TransactionType != "CREDIT" {
			t.Errorf("salary line: in=%v type=%v", salary.MoneyIn, salary.TransactionType)
		}
		if coffee.MoneyOut == nil || *coffee.MoneyOut != "10.08" || coffee.TransactionType == nil || *coffee.TransactionType != "DEBIT" {
			t.Errorf("coffee line: out=%v type=%v", coffee.MoneyOut, coffee.TransactionType)
		}
		if res.Account.CurrentBalance != "17403.51" { // 1000 + 16413.59 - 10.08
			t.Errorf("current balance: got %q, want 17403.51", res.Account.CurrentBalance)
		}

		// Re-importing the same bytes is a no-op (dedupe via external_id).
		res2, err := importCSV(t, user, org, acc.ID, csv)
		if err != nil {
			t.Fatalf("re-import: %v", err)
		}
		if res2.Imported != 0 || res2.SkippedDuplicates != 2 || len(res2.Transactions) != 2 {
			t.Errorf("re-import: got imported=%d skipped=%d txns=%d, want 0/2/2", res2.Imported, res2.SkippedDuplicates, len(res2.Transactions))
		}
	})

	t.Run("statement import: within-file identical lines both import", func(t *testing.T) {
		t.Parallel()
		org, user := newOrgWithOwner(t, ts)
		acc, err := svc.CreateBankAccount(ctx, mustUUID(t, user), mustUUID(t, org), bankReq("Dup", nil))
		if err != nil {
			t.Fatalf("create account: %v", err)
		}
		cleanupBankAccount(t, ts, acc.ID)
		csv := "date,description,amount\n" +
			"11/06/2026,Coffee,-5.00\n" +
			"11/06/2026,Coffee,-5.00\n" // byte-identical line
		res, err := importCSV(t, user, org, acc.ID, csv)
		if err != nil {
			t.Fatalf("import: %v", err)
		}
		if res.Imported != 2 {
			t.Errorf("identical within-file lines should both import: got imported=%d, want 2", res.Imported)
		}
	})

	t.Run("statement import: an invalid CSV is a 422 and imports nothing", func(t *testing.T) {
		t.Parallel()
		org, user := newOrgWithOwner(t, ts)
		acc, err := svc.CreateBankAccount(ctx, mustUUID(t, user), mustUUID(t, org), bankReq("BadCSV", nil))
		if err != nil {
			t.Fatalf("create account: %v", err)
		}
		cleanupBankAccount(t, ts, acc.ID)
		for _, bad := range []string{
			"description,amount\nSalary,10.00\n",                  // missing date column
			"date,description\n11/06/2026,NoAmountCol\n",          // missing amount column
			"date,description,amount\n11/06/2026,Empty,\n",        // empty amount cell
			"date,description,amount\n11/06/2026,Bad,abc\n",       // non-numeric amount
			"date,description,amount\n11/06/2026,Zero,0\n",      // zero amount
			"date,description,amount\nnot-a-date,Bad,10.00\n",   // unparseable date (no layout matches → default DD/MM rejects it)
		} {
			if _, err := importCSV(t, user, org, acc.ID, bad); err == nil {
				t.Errorf("expected 422 for invalid CSV: %q", bad)
			} else {
				assertAppCode(t, err, kernel.ErrCodeValidation)
			}
		}
		if st, _ := svc.ListTransactions(ctx, mustUUID(t, user), mustUUID(t, org), acc.ID); len(st.Transactions) != 0 {
			t.Errorf("a rejected import must insert nothing: got %d transactions", len(st.Transactions))
		}
	})

	t.Run("statement import authz: non-admin 403, other org 404", func(t *testing.T) {
		t.Parallel()
		org, user := newOrgWithOwner(t, ts)
		acc, err := svc.CreateBankAccount(ctx, mustUUID(t, user), mustUUID(t, org), bankReq("ImportAuthz", nil))
		if err != nil {
			t.Fatalf("create account: %v", err)
		}
		cleanupBankAccount(t, ts, acc.ID)
		csv := "date,description,amount\n11/06/2026,X,1.00\n"

		member := addMember(t, ts, org, "member")
		if _, err := importCSV(t, member, org, acc.ID, csv); err == nil {
			t.Error("a non-admin import should be 403")
		} else {
			assertAppCode(t, err, kernel.ErrCodeForbidden)
		}
		otherOrg, otherUser := newOrgWithOwner(t, ts)
		if _, err := importCSV(t, otherUser, otherOrg, acc.ID, csv); err == nil {
			t.Error("importing into another org's account should be 404")
		} else {
			assertAppCode(t, err, kernel.ErrCodeNotFound)
		}
	})

	// --- CSV format auto-detection: preview → confirm mapping → commit ---------

	t.Run("statement preview: Monzo-style signed amount + ISO date is auto-detected", func(t *testing.T) {
		t.Parallel()
		org, user := newOrgWithOwner(t, ts)
		acc, err := svc.CreateBankAccount(ctx, mustUUID(t, user), mustUUID(t, org), bankReq("PreviewMonzo", nil))
		if err != nil {
			t.Fatalf("create account: %v", err)
		}
		cleanupBankAccount(t, ts, acc.ID)

		// Columns that DON'T match our template: 'Name' (not description), a single signed
		// 'Amount', ISO dates.
		csv := "Date,Name,Amount\n" +
			"2026-06-22,Tesco,-12.50\n" +
			"2026-06-23,Acme Ltd,2500.00\n"
		resp, err := svc.PreviewStatement(ctx, mustUUID(t, user), mustUUID(t, org), acc.ID, strings.NewReader(csv), nil)
		if err != nil {
			t.Fatalf("preview: %v", err)
		}
		if resp.Format != "csv" || resp.Mapping == nil {
			t.Fatalf("preview: format=%q mapping=%v, want csv + a mapping", resp.Format, resp.Mapping)
		}
		m := resp.Mapping
		if m.AmountFormat != "signed" || m.AmountColumn == nil || *m.AmountColumn != 2 {
			t.Errorf("amount: format=%q col=%v, want signed/2", m.AmountFormat, m.AmountColumn)
		}
		if m.DateColumn == nil || *m.DateColumn != 0 || m.DescriptionColumn == nil || *m.DescriptionColumn != 1 {
			t.Errorf("date/desc cols: %v/%v, want 0/1", m.DateColumn, m.DescriptionColumn)
		}
		if m.DateFormat != "2006-01-02" {
			t.Errorf("date format: got %q, want ISO 2006-01-02", m.DateFormat)
		}
		// The preview interprets the first row as money OUT (negative signed amount).
		if len(resp.PreviewRows) != 2 || resp.PreviewRows[0].MoneyOut == nil || *resp.PreviewRows[0].MoneyOut != "12.50" {
			t.Errorf("preview row 0: %+v, want money_out 12.50", resp.PreviewRows)
		}
		// Preview must NOT import anything.
		if st, _ := svc.ListTransactions(ctx, mustUUID(t, user), mustUUID(t, org), acc.ID); len(st.Transactions) != 0 {
			t.Errorf("preview must not import: got %d transactions", len(st.Transactions))
		}
	})

	t.Run("statement preview: Barclays-style Debit/Credit split + Balance is auto-detected", func(t *testing.T) {
		t.Parallel()
		org, user := newOrgWithOwner(t, ts)
		acc, err := svc.CreateBankAccount(ctx, mustUUID(t, user), mustUUID(t, org), bankReq("PreviewBarclays", nil))
		if err != nil {
			t.Fatalf("create account: %v", err)
		}
		cleanupBankAccount(t, ts, acc.ID)

		csv := "Date,Description,Debit,Credit,Balance\n" +
			"22/06/2026,Tesco,12.50,,987.50\n" +
			"23/06/2026,Acme,,2500.00,3487.50\n"
		resp, err := svc.PreviewStatement(ctx, mustUUID(t, user), mustUUID(t, org), acc.ID, strings.NewReader(csv), nil)
		if err != nil {
			t.Fatalf("preview: %v", err)
		}
		m := resp.Mapping
		if m == nil || m.AmountFormat != "split" {
			t.Fatalf("amount format: %v, want split", m)
		}
		// Debit is money OUT (col 2), Credit is money IN (col 3).
		if m.MoneyOutColumn == nil || *m.MoneyOutColumn != 2 || m.MoneyInColumn == nil || *m.MoneyInColumn != 3 {
			t.Errorf("split cols: out=%v in=%v, want out=2 (Debit) in=3 (Credit)", m.MoneyOutColumn, m.MoneyInColumn)
		}
		if m.BalanceColumn == nil || *m.BalanceColumn != 4 {
			t.Errorf("balance col: %v, want 4", m.BalanceColumn)
		}
		if m.DateFormat != "02/01/2006" {
			t.Errorf("date format: got %q, want DD/MM 02/01/2006", m.DateFormat)
		}
	})

	t.Run("statement import: a confirmed split mapping signs amount_minor + fills balance", func(t *testing.T) {
		t.Parallel()
		org, user := newOrgWithOwner(t, ts)
		acc, err := svc.CreateBankAccount(ctx, mustUUID(t, user), mustUUID(t, org),
			bankReq("MappedImport", func(r *banking.CreateBankAccountRequest) { r.OpeningBalance = "1000.00" }))
		if err != nil {
			t.Fatalf("create account: %v", err)
		}
		cleanupBankAccount(t, ts, acc.ID)

		// A bank export with cryptic headers the detector wouldn't fully know — so the user
		// confirms the mapping explicitly: split out/in columns, ISO dates, a balance column.
		csv := "When,Memo,Out,In,Bal\n" +
			"2026-06-22,Tesco Stores,12.50,,987.50\n" +
			"2026-06-23,Client Acme,,2500.00,3487.50\n"
		dateCol, descCol, outCol, inCol, balCol := 0, 1, 2, 3, 4
		m := banking.ColumnMapping{
			DateColumn:        &dateCol,
			DescriptionColumn: &descCol,
			AmountFormat:      "split",
			MoneyInColumn:     &inCol,
			MoneyOutColumn:    &outCol,
			BalanceColumn:     &balCol,
			DateFormat:        "2006-01-02",
		}
		res, err := svc.ImportStatement(ctx, mustUUID(t, user), mustUUID(t, org), acc.ID, strings.NewReader(csv), &m)
		if err != nil {
			t.Fatalf("mapped import: %v", err)
		}
		if res.Imported != 2 {
			t.Fatalf("imported: got %d, want 2", res.Imported)
		}
		tesco, acme := res.Transactions[0], res.Transactions[1] // chronological
		if tesco.MoneyOut == nil || *tesco.MoneyOut != "12.50" || tesco.MoneyIn != nil {
			t.Errorf("debit row: out=%v in=%v, want out 12.50", tesco.MoneyOut, tesco.MoneyIn)
		}
		if acme.MoneyIn == nil || *acme.MoneyIn != "2500.00" || acme.MoneyOut != nil {
			t.Errorf("credit row: in=%v out=%v, want in 2500.00", acme.MoneyIn, acme.MoneyOut)
		}
		if res.Account.CurrentBalance != "3487.50" { // 1000 - 12.50 + 2500
			t.Errorf("current balance: got %q, want 3487.50", res.Account.CurrentBalance)
		}
		// The mapped Balance column landed on balance_minor (not surfaced by the API, so check the row).
		var balMinor *int64
		if err := ts.pool.QueryRow(ctx,
			`SELECT balance_minor FROM bank_transactions WHERE bank_account_id=$1 AND description=$2 AND deleted_at IS NULL`,
			mustUUID(t, acc.ID), "Tesco Stores").Scan(&balMinor); err != nil {
			t.Fatalf("balance query: %v", err)
		}
		if balMinor == nil || *balMinor != 98750 {
			t.Errorf("balance_minor: got %v, want 98750", balMinor)
		}
	})

	t.Run("statement preview authz: non-admin 403, other org 404", func(t *testing.T) {
		t.Parallel()
		org, user := newOrgWithOwner(t, ts)
		acc, err := svc.CreateBankAccount(ctx, mustUUID(t, user), mustUUID(t, org), bankReq("PreviewAuthz", nil))
		if err != nil {
			t.Fatalf("create account: %v", err)
		}
		cleanupBankAccount(t, ts, acc.ID)
		csv := "date,description,amount\n11/06/2026,X,1.00\n"

		member := addMember(t, ts, org, "member")
		if _, err := svc.PreviewStatement(ctx, mustUUID(t, member), mustUUID(t, org), acc.ID, strings.NewReader(csv), nil); err == nil {
			t.Error("a non-admin preview should be 403")
		} else {
			assertAppCode(t, err, kernel.ErrCodeForbidden)
		}
		otherOrg, otherUser := newOrgWithOwner(t, ts)
		if _, err := svc.PreviewStatement(ctx, mustUUID(t, otherUser), mustUUID(t, otherOrg), acc.ID, strings.NewReader(csv), nil); err == nil {
			t.Error("previewing another org's account should be 404")
		} else {
			assertAppCode(t, err, kernel.ErrCodeNotFound)
		}
	})

	// --- OFX import (same endpoint, format auto-detected) ---------------------
	// Authz + the 404 path are format-agnostic (they run before parsing in the shared
	// ImportStatement), so the CSV authz test above already covers them.

	t.Run("statement import: valid OFX creates lines, derives sign + type, idempotent", func(t *testing.T) {
		t.Parallel()
		org, user := newOrgWithOwner(t, ts)
		acc, err := svc.CreateBankAccount(ctx, mustUUID(t, user), mustUUID(t, org),
			bankReq("OFXImport", func(r *banking.CreateBankAccountRequest) { r.OpeningBalance = "1000.00" }))
		if err != nil {
			t.Fatalf("create account: %v", err)
		}
		cleanupBankAccount(t, ts, acc.ID)

		// OFX 1.x (SGML): unclosed leaf tags, an already-SIGNED TRNAMT, a FITID per line.
		// The first NAME carries the embedded tab + padding a real Barclays export has,
		// so it also exercises the whitespace cleaning.
		ofx := `OFXHEADER:100
DATA:OFXSGML
VERSION:102

<OFX>
  <BANKMSGSRSV1>
    <STMTTRNRS>
      <STMTRS>
        <CURDEF>GBP
        <BANKACCTFROM>
          <BANKID>492900
          <ACCTID>20254190396605
          <ACCTTYPE>CHECKING
        </BANKACCTFROM>
        <BANKTRANLIST>
          <STMTTRN>
            <TRNTYPE>DIRECTDEP
            <DTPOSTED>20260528000000[-5:EST]
            <TRNAMT>700.00
            <FITID>FIT-CREDIT-1
            <NAME>AXION LONDON LIMIT    ` + "\t" + `S BGC
          </STMTTRN>
          <STMTTRN>
            <TRNTYPE>DIRECTDEBIT
            <DTPOSTED>20260615000000[-5:EST]
            <TRNAMT>-127.42
            <FITID>FIT-DEBIT-1
            <NAME>NOVUNA PERSONAL FI
            <MEMO>Direct debit ref
          </STMTTRN>
        </BANKTRANLIST>
      </STMTRS>
    </STMTTRNRS>
  </BANKMSGSRSV1>
</OFX>`

		res, err := importOFX(t, user, org, acc.ID, ofx)
		if err != nil {
			t.Fatalf("import: %v", err)
		}
		if res.Imported != 2 || res.SkippedDuplicates != 0 || res.Total != 2 {
			t.Errorf("summary: got imported=%d skipped=%d total=%d, want 2/0/2", res.Imported, res.SkippedDuplicates, res.Total)
		}
		if len(res.Transactions) != 2 {
			t.Fatalf("transactions: got %d, want 2", len(res.Transactions))
		}
		credit, debit := res.Transactions[0], res.Transactions[1] // chronological: 28 May before 15 Jun
		if credit.Source != "statement" || credit.Status != "unexplained" {
			t.Errorf("imported classification: source=%q status=%q, want statement/unexplained", credit.Source, credit.Status)
		}
		if credit.MoneyIn == nil || *credit.MoneyIn != "700.00" || credit.TransactionType == nil || *credit.TransactionType != "DIRECTDEP" {
			t.Errorf("credit line: in=%v type=%v", credit.MoneyIn, credit.TransactionType)
		}
		// NAME's embedded tab + run of spaces collapse to single spaces.
		if credit.Description == nil || *credit.Description != "AXION LONDON LIMIT S BGC" {
			t.Errorf("credit description: got %v, want %q", credit.Description, "AXION LONDON LIMIT S BGC")
		}
		if debit.MoneyOut == nil || *debit.MoneyOut != "127.42" || debit.TransactionType == nil || *debit.TransactionType != "DIRECTDEBIT" {
			t.Errorf("debit line: out=%v type=%v", debit.MoneyOut, debit.TransactionType)
		}
		if debit.BankMemo == nil || *debit.BankMemo != "Direct debit ref" {
			t.Errorf("debit bank_memo: got %v, want %q", debit.BankMemo, "Direct debit ref")
		}
		if res.Account.CurrentBalance != "1572.58" { // 1000 + 700 - 127.42
			t.Errorf("current balance: got %q, want 1572.58", res.Account.CurrentBalance)
		}

		// Re-importing the same bytes is a no-op (dedupe via FITID → external_id).
		res2, err := importOFX(t, user, org, acc.ID, ofx)
		if err != nil {
			t.Fatalf("re-import: %v", err)
		}
		if res2.Imported != 0 || res2.SkippedDuplicates != 2 || len(res2.Transactions) != 2 {
			t.Errorf("re-import: got imported=%d skipped=%d txns=%d, want 0/2/2", res2.Imported, res2.SkippedDuplicates, len(res2.Transactions))
		}
	})

	t.Run("statement import: an unknown OFX TRNTYPE maps to OTHER", func(t *testing.T) {
		t.Parallel()
		org, user := newOrgWithOwner(t, ts)
		acc, err := svc.CreateBankAccount(ctx, mustUUID(t, user), mustUUID(t, org), bankReq("OFXType", nil))
		if err != nil {
			t.Fatalf("create account: %v", err)
		}
		cleanupBankAccount(t, ts, acc.ID)
		// HOLD is a real OFX type the bank_transactions CHECK omits → must land as OTHER
		// (else the insert would trip the CHECK).
		ofx := `OFXHEADER:100
<OFX>
  <STMTTRN>
    <TRNTYPE>HOLD
    <DTPOSTED>20260601000000
    <TRNAMT>-9.99
    <FITID>FIT-HOLD-1
    <NAME>Pending authorisation
  </STMTTRN>
</OFX>`
		res, err := importOFX(t, user, org, acc.ID, ofx)
		if err != nil {
			t.Fatalf("import: %v", err)
		}
		if len(res.Transactions) != 1 {
			t.Fatalf("transactions: got %d, want 1", len(res.Transactions))
		}
		if tt := res.Transactions[0].TransactionType; tt == nil || *tt != "OTHER" {
			t.Errorf("unknown TRNTYPE: got %v, want OTHER", tt)
		}
	})

	t.Run("statement import: an OFX with no transactions is a 422 and imports nothing", func(t *testing.T) {
		t.Parallel()
		org, user := newOrgWithOwner(t, ts)
		acc, err := svc.CreateBankAccount(ctx, mustUUID(t, user), mustUUID(t, org), bankReq("OFXEmpty", nil))
		if err != nil {
			t.Fatalf("create account: %v", err)
		}
		cleanupBankAccount(t, ts, acc.ID)
		ofx := `OFXHEADER:100
<OFX>
  <BANKMSGSRSV1>
    <STMTTRNRS>
      <STMTRS>
        <CURDEF>GBP
      </STMTRS>
    </STMTTRNRS>
  </BANKMSGSRSV1>
</OFX>`
		if _, err := importOFX(t, user, org, acc.ID, ofx); err == nil {
			t.Error("expected 422 for an OFX with no transactions")
		} else {
			assertAppCode(t, err, kernel.ErrCodeValidation)
		}
		if st, _ := svc.ListTransactions(ctx, mustUUID(t, user), mustUUID(t, org), acc.ID); len(st.Transactions) != 0 {
			t.Errorf("a rejected import must insert nothing: got %d transactions", len(st.Transactions))
		}
	})
}

// TestExplainService covers the explain/reconcile write path (internal/banking/explain.go):
// the per-type validation, splitting + the over-explain guard, VAT extraction, the Transfer
// and Money-Paid-to-User entity links, and the recompute-driven status/remaining in the
// response. Uses a FRESH ephemeral org with a few hand-seeded CoA categories (the global
// transaction_type_categories mapping already exists for every org).
func TestExplainService(t *testing.T) {
	ts := newTestServer(t)
	t.Cleanup(func() { ts.pool.Close() })
	ctx := context.Background()
	svc := ts.bankingService

	org, user := newOrgWithOwner(t, ts)
	userID, orgID := mustUUID(t, user), mustUUID(t, org)

	// company_type drives the Money-Paid-to-User options (907 is offered for 'limited').
	if _, err := ts.pool.Exec(ctx, `UPDATE organisations SET company_type='limited' WHERE id=$1`, org); err != nil {
		t.Fatalf("set company_type: %v", err)
	}
	// Seed a handful of CoA accounts for this org (the mapping is global; the accounts are per-org).
	seed := func(code, name, accountType, apiGroup string) {
		if _, err := ts.pool.Exec(ctx,
			`INSERT INTO categories (organisation_id, nominal_code, name, account_type, api_group) VALUES ($1,$2,$3,$4,$5)`,
			org, code, name, accountType, apiGroup); err != nil {
			t.Fatalf("seed category %s: %v", code, err)
		}
	}
	seed("254", "Travel and Subsistence", "ADMIN_EXPENSE", "admin_expenses_categories")
	seed("001", "Sales", "INCOME", "income_categories")
	seed("907", "Drawings", "USER_ACCOUNT", "general_categories")
	t.Cleanup(func() {
		_, _ = ts.pool.Exec(context.Background(), `DELETE FROM bank_transaction_explanations WHERE organisation_id=$1`, org)
		_, _ = ts.pool.Exec(context.Background(), `DELETE FROM categories WHERE organisation_id=$1`, org)
	})

	acc, err := svc.CreateBankAccount(ctx, userID, orgID, bankReq("Main", nil))
	if err != nil {
		t.Fatalf("create account: %v", err)
	}
	cleanupBankAccount(t, ts, acc.ID)
	acc2, err := svc.CreateBankAccount(ctx, userID, orgID, bankReq("Savings", nil))
	if err != nil {
		t.Fatalf("create account 2: %v", err)
	}
	cleanupBankAccount(t, ts, acc2.ID)

	catID := func(code string) string {
		var id string
		if err := ts.pool.QueryRow(ctx, `SELECT id::text FROM categories WHERE organisation_id=$1 AND nominal_code=$2`, org, code).Scan(&id); err != nil {
			t.Fatalf("catID %s: %v", code, err)
		}
		return id
	}
	newTxn := func(accID string, amountMinor int64) string {
		var id string
		if err := ts.pool.QueryRow(ctx,
			`INSERT INTO bank_transactions (organisation_id, bank_account_id, dated_on, amount_minor, status, source)
			 VALUES ($1,$2,CURRENT_DATE,$3,'unexplained','manual') RETURNING id::text`, org, accID, amountMinor).Scan(&id); err != nil {
			t.Fatalf("new txn: %v", err)
		}
		return id
	}
	ptr := func(s string) *string { return &s }

	t.Run("Payment fully explains a money-out line", func(t *testing.T) {
		txn := newTxn(acc.ID, -12000) // £120 out
		resp, err := svc.CreateExplanation(ctx, userID, orgID, acc.ID, txn, banking.CreateExplanationRequest{
			Type: "PAYMENT", Amount: "120.00", CategoryID: ptr(catID("254")),
		})
		if err != nil {
			t.Fatalf("explain: %v", err)
		}
		if resp.Status != "explained" || resp.UnexplainedAmount != "0.00" {
			t.Errorf("got status=%q unexplained=%q, want explained / 0.00", resp.Status, resp.UnexplainedAmount)
		}
		if len(resp.Explanations) != 1 || resp.Explanations[0].Amount != "120.00" {
			t.Fatalf("explanations: %+v", resp.Explanations)
		}
	})

	t.Run("fixed VAT rate extracts the VAT and ignores a sent amount", func(t *testing.T) {
		var vatID string
		if err := ts.pool.QueryRow(ctx, `SELECT id::text FROM vat_rates WHERE rate_bps=2000 AND is_fixed_ratio=true AND country_code='GB' LIMIT 1`).Scan(&vatID); err != nil {
			t.Skipf("no GB 20%% fixed VAT rate seeded: %v", err)
		}
		txn := newTxn(acc.ID, -12000) // £120 incl 20% → £20 VAT
		resp, err := svc.CreateExplanation(ctx, userID, orgID, acc.ID, txn, banking.CreateExplanationRequest{
			Type: "PAYMENT", Amount: "120.00", CategoryID: ptr(catID("254")), VATRateID: ptr(vatID), VATAmount: ptr("999.00"),
		})
		if err != nil {
			t.Fatalf("explain: %v", err)
		}
		if resp.Explanations[0].VATValue != "20.00" { // computed — the sent 999.00 is ignored for a fixed rate
			t.Errorf("VAT value: got %q, want 20.00 (computed; sent amount ignored)", resp.Explanations[0].VATValue)
		}
	})

	t.Run("manual VAT rate stores the typed amount + is_manual flag", func(t *testing.T) {
		var vatID string
		if err := ts.pool.QueryRow(ctx, `SELECT id::text FROM vat_rates WHERE is_fixed_ratio=false AND country_code='GB' LIMIT 1`).Scan(&vatID); err != nil {
			t.Skipf("no GB manual VAT rate seeded: %v", err)
		}
		txn := newTxn(acc.ID, -12000) // £120 out; the user types £10 VAT (≠ the 20% = £20)
		resp, err := svc.CreateExplanation(ctx, userID, orgID, acc.ID, txn, banking.CreateExplanationRequest{
			Type: "PAYMENT", Amount: "120.00", CategoryID: ptr(catID("254")), VATRateID: ptr(vatID), VATAmount: ptr("10.00"),
		})
		if err != nil {
			t.Fatalf("explain: %v", err)
		}
		if resp.Explanations[0].VATValue != "10.00" {
			t.Errorf("manual VAT value: got %q, want 10.00 (the typed amount)", resp.Explanations[0].VATValue)
		}
		var isManual bool
		if err := ts.pool.QueryRow(ctx, `SELECT is_manual_sales_tax FROM bank_transaction_explanations WHERE id=$1`, resp.Explanations[0].ID).Scan(&isManual); err != nil {
			t.Fatalf("read is_manual_sales_tax: %v", err)
		}
		if !isManual {
			t.Error("is_manual_sales_tax should be TRUE for a manual rate")
		}
	})

	t.Run("split across two portions sums to fully explained", func(t *testing.T) {
		txn := newTxn(acc.ID, -10000) // £100 out
		r1, err := svc.CreateExplanation(ctx, userID, orgID, acc.ID, txn, banking.CreateExplanationRequest{Type: "PAYMENT", Amount: "60.00", CategoryID: ptr(catID("254"))})
		if err != nil {
			t.Fatalf("first split: %v", err)
		}
		if r1.Status != "unexplained" || r1.UnexplainedAmount != "-40.00" {
			t.Errorf("after first split: status=%q remaining=%q, want unexplained / -40.00", r1.Status, r1.UnexplainedAmount)
		}
		r2, err := svc.CreateExplanation(ctx, userID, orgID, acc.ID, txn, banking.CreateExplanationRequest{Type: "PAYMENT", Amount: "40.00", CategoryID: ptr(catID("254"))})
		if err != nil {
			t.Fatalf("second split: %v", err)
		}
		if r2.Status != "explained" || r2.UnexplainedAmount != "0.00" || len(r2.Explanations) != 2 {
			t.Errorf("after second split: status=%q remaining=%q n=%d", r2.Status, r2.UnexplainedAmount, len(r2.Explanations))
		}
	})

	t.Run("over-explaining is rejected", func(t *testing.T) {
		txn := newTxn(acc.ID, -5000) // £50 out
		_, err := svc.CreateExplanation(ctx, userID, orgID, acc.ID, txn, banking.CreateExplanationRequest{Type: "PAYMENT", Amount: "60.00", CategoryID: ptr(catID("254"))})
		assertAppCode(t, err, kernel.ErrCodeValidation)
	})

	t.Run("direction mismatch is rejected", func(t *testing.T) {
		txn := newTxn(acc.ID, -5000)                                                                                                                                      // money OUT
		_, err := svc.CreateExplanation(ctx, userID, orgID, acc.ID, txn, banking.CreateExplanationRequest{Type: "SALES", Amount: "50.00", CategoryID: ptr(catID("001"))}) // SALES is money-in
		assertAppCode(t, err, kernel.ErrCodeValidation)
	})

	t.Run("unsupported (future-entity) type is rejected", func(t *testing.T) {
		txn := newTxn(acc.ID, -5000)
		// CREDIT_NOTE_REFUND (entity_link CREDIT_NOTE) is still future-entity (BILL_PAYMENT is now supported).
		_, err := svc.CreateExplanation(ctx, userID, orgID, acc.ID, txn, banking.CreateExplanationRequest{Type: "CREDIT_NOTE_REFUND", Amount: "50.00", CategoryID: ptr(catID("254"))})
		assertAppCode(t, err, kernel.ErrCodeValidation)
	})

	t.Run("category not offered for the type is rejected", func(t *testing.T) {
		txn := newTxn(acc.ID, -5000)
		_, err := svc.CreateExplanation(ctx, userID, orgID, acc.ID, txn, banking.CreateExplanationRequest{Type: "PAYMENT", Amount: "50.00", CategoryID: ptr(catID("001"))}) // income cat under Payment
		assertAppCode(t, err, kernel.ErrCodeValidation)
	})

	t.Run("Transfer to another account", func(t *testing.T) {
		txn := newTxn(acc.ID, -7500) // £75 out
		resp, err := svc.CreateExplanation(ctx, userID, orgID, acc.ID, txn, banking.CreateExplanationRequest{Type: "TRANSFER_TO_ACCOUNT", Amount: "75.00", TransferBankAccountID: ptr(acc2.ID)})
		if err != nil {
			t.Fatalf("transfer: %v", err)
		}
		if resp.Status != "explained" {
			t.Errorf("status=%q, want explained", resp.Status)
		}
		if resp.Explanations[0].TransferBankAccountID == nil || *resp.Explanations[0].TransferBankAccountID != acc2.ID {
			t.Errorf("transfer account not set: %+v", resp.Explanations[0])
		}
	})

	t.Run("Transfer to the same account is rejected", func(t *testing.T) {
		txn := newTxn(acc.ID, -7500)
		_, err := svc.CreateExplanation(ctx, userID, orgID, acc.ID, txn, banking.CreateExplanationRequest{Type: "TRANSFER_TO_ACCOUNT", Amount: "75.00", TransferBankAccountID: ptr(acc.ID)})
		assertAppCode(t, err, kernel.ErrCodeValidation)
	})

	t.Run("Money Paid to User (member + account 907)", func(t *testing.T) {
		member := addMember(t, ts, org, "admin")
		txn := newTxn(acc.ID, -30000) // £300 out
		resp, err := svc.CreateExplanation(ctx, userID, orgID, acc.ID, txn, banking.CreateExplanationRequest{
			Type: "MONEY_PAID_TO_USER", Amount: "300.00", CategoryID: ptr(catID("907")), PaidUserID: ptr(member),
		})
		if err != nil {
			t.Fatalf("user payment: %v", err)
		}
		if resp.Status != "explained" {
			t.Errorf("status=%q, want explained", resp.Status)
		}
		if resp.Explanations[0].PaidUserID == nil || *resp.Explanations[0].PaidUserID != member {
			t.Errorf("paid user not set: %+v", resp.Explanations[0])
		}
	})

	t.Run("soft-delete re-opens the remainder", func(t *testing.T) {
		txn := newTxn(acc.ID, -10000)
		r1, err := svc.CreateExplanation(ctx, userID, orgID, acc.ID, txn, banking.CreateExplanationRequest{Type: "PAYMENT", Amount: "100.00", CategoryID: ptr(catID("254"))})
		if err != nil || r1.Status != "explained" {
			t.Fatalf("setup: status=%q err=%v", r1.Status, err)
		}
		r2, err := svc.DeleteExplanation(ctx, userID, orgID, acc.ID, txn, r1.Explanations[0].ID)
		if err != nil {
			t.Fatalf("delete: %v", err)
		}
		if r2.Status != "unexplained" || len(r2.Explanations) != 0 {
			t.Errorf("after delete: status=%q n=%d, want unexplained / 0", r2.Status, len(r2.Explanations))
		}
	})

	t.Run("explaining another org's transaction is 404", func(t *testing.T) {
		otherOrg, otherUser := newOrgWithOwner(t, ts)
		txn := newTxn(acc.ID, -5000) // belongs to `org`
		_, err := svc.CreateExplanation(ctx, mustUUID(t, otherUser), mustUUID(t, otherOrg), acc.ID, txn, banking.CreateExplanationRequest{Type: "PAYMENT", Amount: "50.00", CategoryID: ptr(catID("254"))})
		assertAppCode(t, err, kernel.ErrCodeNotFound)
	})

	t.Run("a non-admin cannot explain", func(t *testing.T) {
		memberID := addMember(t, ts, org, "member")
		txn := newTxn(acc.ID, -5000)
		_, err := svc.CreateExplanation(ctx, mustUUID(t, memberID), orgID, acc.ID, txn, banking.CreateExplanationRequest{Type: "PAYMENT", Amount: "50.00", CategoryID: ptr(catID("254"))})
		assertAppCode(t, err, kernel.ErrCodeForbidden)
	})
}

// =============================================================================
// Invoice Receipt — explaining a money-IN bank line against a sent sales invoice.
// Exercises the cross-domain sync (banking explanation → invoices.paid_value_minor),
// the overpayment cap, and the validation rules. Real Postgres; the invoice is seeded
// directly so its total + status are exact (no dependence on line-item VAT math).
// =============================================================================
func TestInvoiceReceiptExplain(t *testing.T) {
	ts := newTestServer(t)
	t.Cleanup(func() { ts.pool.Close() })
	ctx := context.Background()
	svc := ts.bankingService

	org, user := newOrgWithOwner(t, ts)
	userID, orgID := mustUUID(t, user), mustUUID(t, org)

	acc, err := svc.CreateBankAccount(ctx, userID, orgID, bankReq("Main", nil))
	if err != nil {
		t.Fatalf("create account: %v", err)
	}
	cleanupBankAccount(t, ts, acc.ID)
	contactID := createContactAs(t, ts, user, org)

	// Registered AFTER the account + contact so LIFO deletes in FK order: explanations
	// → invoices (here) → contact (createContactAs) → bank_transactions + account.
	t.Cleanup(func() {
		bg := context.Background()
		_, _ = ts.pool.Exec(bg, `DELETE FROM bank_transaction_explanations WHERE organisation_id=$1`, org)
		_, _ = ts.pool.Exec(bg, `DELETE FROM invoices WHERE organisation_id=$1`, org)
	})

	ptr := func(s string) *string { return &s }
	// newTxn inserts one bank line (signed minor units: + in / - out) on the account.
	newTxn := func(amountMinor int64) string {
		var id string
		if err := ts.pool.QueryRow(ctx,
			`INSERT INTO bank_transactions (organisation_id, bank_account_id, dated_on, amount_minor, status, source)
			 VALUES ($1,$2,CURRENT_DATE,$3,'unexplained','manual') RETURNING id::text`, org, acc.ID, amountMinor).Scan(&id); err != nil {
			t.Fatalf("new txn: %v", err)
		}
		return id
	}
	// newInvoice seeds a sales invoice with an exact total + status (no VAT, no lines).
	newInvoice := func(status string, totalMinor int64) string {
		var id string
		if err := ts.pool.QueryRow(ctx,
			`INSERT INTO invoices (organisation_id, created_by_user_id, contact_id, dated_on, status,
			        net_value_minor, sales_tax_value_minor, total_value_minor, reference)
			 VALUES ($1,$2,$3,CURRENT_DATE,$4,$5,0,$5,$6) RETURNING id::text`,
			org, user, contactID, status, totalMinor, randomRef()).Scan(&id); err != nil {
			t.Fatalf("seed invoice: %v", err)
		}
		return id
	}
	paidOf := func(invID string) int64 {
		var paid int64
		if err := ts.pool.QueryRow(ctx, `SELECT paid_value_minor FROM invoices WHERE id=$1`, invID).Scan(&paid); err != nil {
			t.Fatalf("read paid: %v", err)
		}
		return paid
	}
	displayOf := func(invID string) string {
		detail, err := ts.invoiceService.GetInvoice(ctx, userID, orgID, invID)
		if err != nil {
			t.Fatalf("get invoice: %v", err)
		}
		return detail.DisplayStatus
	}
	receipt := func(amount string, invID *string) banking.CreateExplanationRequest {
		return banking.CreateExplanationRequest{Type: "INVOICE_RECEIPT", Amount: amount, PaidInvoiceID: invID}
	}

	t.Run("full receipt records the payment and derives Paid", func(t *testing.T) {
		inv := newInvoice("SENT", 20000) // £200 owed
		txn := newTxn(20000)             // £200 in
		resp, err := svc.CreateExplanation(ctx, userID, orgID, acc.ID, txn, receipt("200.00", ptr(inv)))
		if err != nil {
			t.Fatalf("explain: %v", err)
		}
		if resp.Status != "explained" || resp.UnexplainedAmount != "0.00" {
			t.Errorf("line: status=%q unexplained=%q, want explained / 0.00", resp.Status, resp.UnexplainedAmount)
		}
		if got := paidOf(inv); got != 20000 {
			t.Errorf("paid_value_minor: got %d, want 20000", got)
		}
		if got := displayOf(inv); got != "Paid" {
			t.Errorf("display status: got %q, want Paid", got)
		}
		e := resp.Explanations[0]
		if e.PaidInvoiceID == nil || *e.PaidInvoiceID != inv {
			t.Errorf("paid_invoice_id not echoed: %+v", e)
		}
		if e.CategoryID != nil {
			t.Errorf("an invoice receipt should carry no category, got %v", *e.CategoryID)
		}
	})

	t.Run("partial receipts accumulate; Open until fully paid", func(t *testing.T) {
		inv := newInvoice("SENT", 10000) // £100 owed
		if _, err := svc.CreateExplanation(ctx, userID, orgID, acc.ID, newTxn(4000), receipt("40.00", ptr(inv))); err != nil {
			t.Fatalf("first receipt: %v", err)
		}
		if got := paidOf(inv); got != 4000 {
			t.Errorf("paid after £40: got %d, want 4000", got)
		}
		if got := displayOf(inv); got != "Open" {
			t.Errorf("display after partial: got %q, want Open", got)
		}
		if _, err := svc.CreateExplanation(ctx, userID, orgID, acc.ID, newTxn(6000), receipt("60.00", ptr(inv))); err != nil {
			t.Fatalf("second receipt: %v", err)
		}
		if got := paidOf(inv); got != 10000 {
			t.Errorf("paid after £100: got %d, want 10000", got)
		}
		if got := displayOf(inv); got != "Paid" {
			t.Errorf("display after full: got %q, want Paid", got)
		}
	})

	t.Run("deleting a receipt restores the invoice's paid value", func(t *testing.T) {
		inv := newInvoice("SENT", 5000)
		txn := newTxn(5000)
		r, err := svc.CreateExplanation(ctx, userID, orgID, acc.ID, txn, receipt("50.00", ptr(inv)))
		if err != nil {
			t.Fatalf("explain: %v", err)
		}
		if _, err := svc.DeleteExplanation(ctx, userID, orgID, acc.ID, txn, r.Explanations[0].ID); err != nil {
			t.Fatalf("delete: %v", err)
		}
		if got := paidOf(inv); got != 0 {
			t.Errorf("paid after delete: got %d, want 0", got)
		}
	})

	t.Run("editing the receipt amount re-syncs paid", func(t *testing.T) {
		inv := newInvoice("SENT", 10000)
		txn := newTxn(10000) // £100 line so the portion can shrink
		r, err := svc.CreateExplanation(ctx, userID, orgID, acc.ID, txn, receipt("100.00", ptr(inv)))
		if err != nil {
			t.Fatalf("explain: %v", err)
		}
		if _, err := svc.UpdateExplanation(ctx, userID, orgID, acc.ID, txn, r.Explanations[0].ID, receipt("60.00", ptr(inv))); err != nil {
			t.Fatalf("edit: %v", err)
		}
		if got := paidOf(inv); got != 6000 {
			t.Errorf("paid after edit to £60: got %d, want 6000", got)
		}
	})

	t.Run("re-pointing a receipt moves paid between invoices", func(t *testing.T) {
		invA := newInvoice("SENT", 10000)
		invB := newInvoice("SENT", 10000)
		txn := newTxn(5000)
		r, err := svc.CreateExplanation(ctx, userID, orgID, acc.ID, txn, receipt("50.00", ptr(invA)))
		if err != nil {
			t.Fatalf("explain: %v", err)
		}
		if _, err := svc.UpdateExplanation(ctx, userID, orgID, acc.ID, txn, r.Explanations[0].ID, receipt("50.00", ptr(invB))); err != nil {
			t.Fatalf("re-point: %v", err)
		}
		if pa, pb := paidOf(invA), paidOf(invB); pa != 0 || pb != 5000 {
			t.Errorf("after re-point: A=%d B=%d, want A=0 B=5000", pa, pb)
		}
	})

	t.Run("a receipt blocks reopening the invoice until it is removed", func(t *testing.T) {
		inv := newInvoice("SENT", 10000)
		txn := newTxn(6000)
		r, err := svc.CreateExplanation(ctx, userID, orgID, acc.ID, txn, receipt("60.00", ptr(inv))) // partially paid
		if err != nil {
			t.Fatalf("explain: %v", err)
		}
		// With a receipt against it, reopen (SENT → DRAFT) is a 409.
		if _, err := ts.invoiceService.ChangeStatus(ctx, userID, orgID, inv, "reopen"); err == nil {
			t.Fatal("expected reopen to be blocked while a receipt exists")
		} else {
			assertAppCode(t, err, kernel.ErrCodeConflict)
		}
		// Remove the receipt → paid back to 0 → reopen now succeeds.
		if _, err := svc.DeleteExplanation(ctx, userID, orgID, acc.ID, txn, r.Explanations[0].ID); err != nil {
			t.Fatalf("delete receipt: %v", err)
		}
		out, err := ts.invoiceService.ChangeStatus(ctx, userID, orgID, inv, "reopen")
		if err != nil {
			t.Fatalf("reopen after removing receipt: %v", err)
		}
		if out.Status != "DRAFT" {
			t.Errorf("status after reopen: got %q, want DRAFT", out.Status)
		}
	})

	t.Run("editing the same receipt to the same amount is allowed", func(t *testing.T) {
		inv := newInvoice("SENT", 5000)
		txn := newTxn(5000)
		r, err := svc.CreateExplanation(ctx, userID, orgID, acc.ID, txn, receipt("50.00", ptr(inv))) // fully pays it
		if err != nil {
			t.Fatalf("explain: %v", err)
		}
		// Re-saving £50 against the now-fully-paid invoice must NOT trip the cap (its own
		// prior portion is given back before the check).
		if _, err := svc.UpdateExplanation(ctx, userID, orgID, acc.ID, txn, r.Explanations[0].ID, receipt("50.00", ptr(inv))); err != nil {
			t.Fatalf("re-save same amount: %v", err)
		}
		if got := paidOf(inv); got != 5000 {
			t.Errorf("paid after re-save: got %d, want 5000", got)
		}
	})

	t.Run("paid_invoice_id is required", func(t *testing.T) {
		_, err := svc.CreateExplanation(ctx, userID, orgID, acc.ID, newTxn(5000), receipt("50.00", nil))
		assertAppCode(t, err, kernel.ErrCodeValidation)
	})

	t.Run("a draft invoice cannot be paid", func(t *testing.T) {
		inv := newInvoice("DRAFT", 5000)
		_, err := svc.CreateExplanation(ctx, userID, orgID, acc.ID, newTxn(5000), receipt("50.00", ptr(inv)))
		assertAppCode(t, err, kernel.ErrCodeValidation)
	})

	t.Run("a fully-paid invoice cannot be paid again", func(t *testing.T) {
		inv := newInvoice("SENT", 5000)
		if _, err := ts.pool.Exec(ctx, `UPDATE invoices SET paid_value_minor=5000 WHERE id=$1`, inv); err != nil {
			t.Fatalf("mark paid: %v", err)
		}
		_, err := svc.CreateExplanation(ctx, userID, orgID, acc.ID, newTxn(5000), receipt("50.00", ptr(inv)))
		assertAppCode(t, err, kernel.ErrCodeValidation)
	})

	t.Run("overpayment beyond the outstanding balance is rejected", func(t *testing.T) {
		inv := newInvoice("SENT", 5000) // £50 owed
		txn := newTxn(10000)            // £100 in — the bank line allows £100, but the invoice only owes £50
		_, err := svc.CreateExplanation(ctx, userID, orgID, acc.ID, txn, receipt("100.00", ptr(inv)))
		assertAppCode(t, err, kernel.ErrCodeValidation)
	})

	t.Run("an invoice receipt on a money-out line is rejected", func(t *testing.T) {
		inv := newInvoice("SENT", 5000)
		_, err := svc.CreateExplanation(ctx, userID, orgID, acc.ID, newTxn(-5000), receipt("50.00", ptr(inv)))
		assertAppCode(t, err, kernel.ErrCodeValidation)
	})

	t.Run("another org's invoice is not payable (org-scoped)", func(t *testing.T) {
		otherOrg, otherUser := newOrgWithOwner(t, ts)
		otherContact := createContactAs(t, ts, otherUser, otherOrg)
		var foreign string
		if err := ts.pool.QueryRow(ctx,
			`INSERT INTO invoices (organisation_id, created_by_user_id, contact_id, dated_on, status,
			        net_value_minor, sales_tax_value_minor, total_value_minor, reference)
			 VALUES ($1,$2,$3,CURRENT_DATE,'SENT',5000,0,5000,$4) RETURNING id::text`,
			otherOrg, otherUser, otherContact, randomRef()).Scan(&foreign); err != nil {
			t.Fatalf("seed foreign invoice: %v", err)
		}
		t.Cleanup(func() { _, _ = ts.pool.Exec(context.Background(), `DELETE FROM invoices WHERE id=$1`, foreign) })
		// Caller is `org`; the invoice belongs to otherOrg → the org-scoped GetInvoice misses → 422.
		_, err := svc.CreateExplanation(ctx, userID, orgID, acc.ID, newTxn(5000), receipt("50.00", ptr(foreign)))
		assertAppCode(t, err, kernel.ErrCodeValidation)
	})
}

// TestBillPaymentExplain mirrors TestInvoiceReceiptExplain on the money-OUT side:
// explaining a bank line as BILL_PAYMENT settles an unpaid bill and keeps the bill's
// paid_value_minor in sync. Key difference: a money-out gross is negative, so the
// service negates the sum → a POSITIVE paid value, and the overpayment guard compares
// magnitudes. Real Postgres via the harness.
func TestBillPaymentExplain(t *testing.T) {
	ts := newTestServer(t)
	t.Cleanup(func() { ts.pool.Close() })
	ctx := context.Background()
	svc := ts.bankingService

	org, user := newOrgWithOwner(t, ts)
	userID, orgID := mustUUID(t, user), mustUUID(t, org)

	acc, err := svc.CreateBankAccount(ctx, userID, orgID, bankReq("Main", nil))
	if err != nil {
		t.Fatalf("create account: %v", err)
	}
	cleanupBankAccount(t, ts, acc.ID)
	contactID := createContactAs(t, ts, user, org)

	// A spending category for the fresh org (bills require category_id NOT NULL).
	var catID string
	if err := ts.pool.QueryRow(ctx,
		`INSERT INTO categories (organisation_id, nominal_code, name, account_type)
		 VALUES ($1,'254','Test Spend','ADMIN_EXPENSE') RETURNING id::text`, org).Scan(&catID); err != nil {
		t.Fatalf("seed category: %v", err)
	}

	// LIFO cleanup: explanations → bills → categories (bills.category_id FK) before the
	// contact (createContactAs) + account cleanups registered earlier.
	t.Cleanup(func() {
		bg := context.Background()
		_, _ = ts.pool.Exec(bg, `DELETE FROM bank_transaction_explanations WHERE organisation_id=$1`, org)
		_, _ = ts.pool.Exec(bg, `DELETE FROM bills WHERE organisation_id=$1`, org)
		_, _ = ts.pool.Exec(bg, `DELETE FROM categories WHERE organisation_id=$1`, org)
	})

	ptr := func(s string) *string { return &s }
	newTxn := func(amountMinor int64) string {
		var id string
		if err := ts.pool.QueryRow(ctx,
			`INSERT INTO bank_transactions (organisation_id, bank_account_id, dated_on, amount_minor, status, source)
			 VALUES ($1,$2,CURRENT_DATE,$3,'unexplained','manual') RETURNING id::text`, org, acc.ID, amountMinor).Scan(&id); err != nil {
			t.Fatalf("new txn: %v", err)
		}
		return id
	}
	newBill := func(totalMinor int64) string {
		var id string
		if err := ts.pool.QueryRow(ctx,
			`INSERT INTO bills (organisation_id, created_by_user_id, contact_id, dated_on, category_id,
			        net_value_minor, sales_tax_value_minor, total_value_minor, reference)
			 VALUES ($1,$2,$3,CURRENT_DATE,$4,$5,0,$5,$6) RETURNING id::text`,
			org, user, contactID, catID, totalMinor, randomRef()).Scan(&id); err != nil {
			t.Fatalf("seed bill: %v", err)
		}
		return id
	}
	paidOf := func(billID string) int64 {
		var p int64
		if err := ts.pool.QueryRow(ctx, `SELECT paid_value_minor FROM bills WHERE id=$1`, billID).Scan(&p); err != nil {
			t.Fatalf("read paid: %v", err)
		}
		return p
	}
	dueOf := func(billID string) int64 {
		var d int64
		if err := ts.pool.QueryRow(ctx, `SELECT due_value_minor FROM bills WHERE id=$1`, billID).Scan(&d); err != nil {
			t.Fatalf("read due: %v", err)
		}
		return d
	}
	pay := func(amount string, billID *string) banking.CreateExplanationRequest {
		return banking.CreateExplanationRequest{Type: "BILL_PAYMENT", Amount: amount, PaidBillID: billID}
	}

	t.Run("full payment records paid (positive) + drives due to 0", func(t *testing.T) {
		bill := newBill(12000) // £120 owed
		txn := newTxn(-12000)  // £120 OUT
		resp, err := svc.CreateExplanation(ctx, userID, orgID, acc.ID, txn, pay("120.00", ptr(bill)))
		if err != nil {
			t.Fatalf("explain: %v", err)
		}
		if resp.Status != "explained" || resp.UnexplainedAmount != "0.00" {
			t.Errorf("line: status=%q unexplained=%q, want explained / 0.00", resp.Status, resp.UnexplainedAmount)
		}
		if got := paidOf(bill); got != 12000 {
			t.Errorf("paid_value_minor: got %d, want 12000 (positive)", got)
		}
		if got := dueOf(bill); got != 0 {
			t.Errorf("due_value_minor: got %d, want 0", got)
		}
		e := resp.Explanations[0]
		if e.PaidBillID == nil || *e.PaidBillID != bill {
			t.Errorf("paid_bill_id not echoed: %+v", e)
		}
		if e.CategoryID != nil {
			t.Errorf("a bill payment should carry no category, got %v", *e.CategoryID)
		}
	})

	t.Run("partial payments accumulate; due shrinks", func(t *testing.T) {
		bill := newBill(10000) // £100
		if _, err := svc.CreateExplanation(ctx, userID, orgID, acc.ID, newTxn(-4000), pay("40.00", ptr(bill))); err != nil {
			t.Fatalf("first payment: %v", err)
		}
		if got := paidOf(bill); got != 4000 {
			t.Errorf("paid after £40: got %d, want 4000", got)
		}
		if got := dueOf(bill); got != 6000 {
			t.Errorf("due after £40: got %d, want 6000", got)
		}
		if _, err := svc.CreateExplanation(ctx, userID, orgID, acc.ID, newTxn(-6000), pay("60.00", ptr(bill))); err != nil {
			t.Fatalf("second payment: %v", err)
		}
		if got := paidOf(bill); got != 10000 {
			t.Errorf("paid after £100: got %d, want 10000", got)
		}
		if got := dueOf(bill); got != 0 {
			t.Errorf("due after £100: got %d, want 0", got)
		}
	})

	t.Run("overpayment is rejected", func(t *testing.T) {
		bill := newBill(5000) // £50 owed
		txn := newTxn(-10000) // £100 line (room to over-explain → the cap is the bill's outstanding)
		_, err := svc.CreateExplanation(ctx, userID, orgID, acc.ID, txn, pay("60.00", ptr(bill)))
		assertAppCode(t, err, kernel.ErrCodeValidation)
		if got := paidOf(bill); got != 0 {
			t.Errorf("paid after rejected overpayment: got %d, want 0", got)
		}
	})

	t.Run("deleting a payment restores paid to 0", func(t *testing.T) {
		bill := newBill(5000)
		txn := newTxn(-5000)
		r, err := svc.CreateExplanation(ctx, userID, orgID, acc.ID, txn, pay("50.00", ptr(bill)))
		if err != nil {
			t.Fatalf("explain: %v", err)
		}
		if _, err := svc.DeleteExplanation(ctx, userID, orgID, acc.ID, txn, r.Explanations[0].ID); err != nil {
			t.Fatalf("delete: %v", err)
		}
		if got := paidOf(bill); got != 0 {
			t.Errorf("paid after delete: got %d, want 0", got)
		}
	})

	t.Run("editing the payment amount re-syncs paid", func(t *testing.T) {
		bill := newBill(10000)
		txn := newTxn(-10000)
		r, err := svc.CreateExplanation(ctx, userID, orgID, acc.ID, txn, pay("100.00", ptr(bill)))
		if err != nil {
			t.Fatalf("explain: %v", err)
		}
		if _, err := svc.UpdateExplanation(ctx, userID, orgID, acc.ID, txn, r.Explanations[0].ID, pay("60.00", ptr(bill))); err != nil {
			t.Fatalf("edit: %v", err)
		}
		if got := paidOf(bill); got != 6000 {
			t.Errorf("paid after edit to £60: got %d, want 6000", got)
		}
	})
}

// TestExplanationFiledPeriodLock covers the bank-explanation half of the VAT
// filed-period lock: once a return covering a date is filed, an explanation dated
// inside that period can no longer be created, edited, deleted, or MOVED into it
// (409 conflict), while explanations in unfiled periods are unaffected. The period
// is "filed" by inserting a marked_as_filed vat_returns row — all the guard reads.
func TestExplanationFiledPeriodLock(t *testing.T) {
	ts := newTestServer(t)
	t.Cleanup(func() { ts.pool.Close() })
	ctx := context.Background()
	svc := ts.bankingService

	org, user := newOrgWithOwner(t, ts)
	userID, orgID := mustUUID(t, user), mustUUID(t, org)

	// One CoA expense account for the PAYMENT explanations (the mapping is global; the
	// account is per-org), plus cleanup of everything this test writes.
	if _, err := ts.pool.Exec(ctx,
		`INSERT INTO categories (organisation_id, nominal_code, name, account_type, api_group)
		 VALUES ($1,'254','Travel and Subsistence','ADMIN_EXPENSE','admin_expenses_categories')`, org); err != nil {
		t.Fatalf("seed category: %v", err)
	}
	t.Cleanup(func() {
		c := context.Background()
		_, _ = ts.pool.Exec(c, `DELETE FROM bank_transaction_explanations WHERE organisation_id=$1`, org)
		_, _ = ts.pool.Exec(c, `DELETE FROM vat_returns WHERE organisation_id=$1`, org)
		_, _ = ts.pool.Exec(c, `DELETE FROM categories WHERE organisation_id=$1`, org)
	})

	acc, err := svc.CreateBankAccount(ctx, userID, orgID, bankReq("Main", nil))
	if err != nil {
		t.Fatalf("create account: %v", err)
	}
	cleanupBankAccount(t, ts, acc.ID)

	var catID string
	if err := ts.pool.QueryRow(ctx, `SELECT id::text FROM categories WHERE organisation_id=$1 AND nominal_code='254'`, org).Scan(&catID); err != nil {
		t.Fatalf("catID: %v", err)
	}
	ptr := func(s string) *string { return &s }
	payment := banking.CreateExplanationRequest{Type: "PAYMENT", Amount: "120.00", CategoryID: ptr(catID)}

	newTxnOn := func(datedOn string, amountMinor int64) string {
		var id string
		if err := ts.pool.QueryRow(ctx,
			`INSERT INTO bank_transactions (organisation_id, bank_account_id, dated_on, amount_minor, status, source)
			 VALUES ($1,$2,$3,$4,'unexplained','manual') RETURNING id::text`, org, acc.ID, datedOn, amountMinor).Scan(&id); err != nil {
			t.Fatalf("new txn %s: %v", datedOn, err)
		}
		return id
	}

	// A money-out line dated INSIDE the soon-to-be-filed quarter, explained while open.
	txnIn := newTxnOn("2026-04-12", -12000)
	respIn, err := svc.CreateExplanation(ctx, userID, orgID, acc.ID, txnIn, payment)
	if err != nil {
		t.Fatalf("explain before filing: %v", err)
	}
	explIn := respIn.Explanations[0].ID

	// A money-out line dated in a LATER, never-filed quarter, explained too.
	txnOut := newTxnOn("2026-07-15", -12000)
	respOut, err := svc.CreateExplanation(ctx, userID, orgID, acc.ID, txnOut, payment)
	if err != nil {
		t.Fatalf("explain (later quarter): %v", err)
	}
	explOut := respOut.Explanations[0].ID

	// File the Mar–May 2026 quarter: a marked_as_filed snapshot covering txnIn's date.
	if _, err := ts.pool.Exec(ctx,
		`INSERT INTO vat_returns (organisation_id, created_by_user_id, period_start, period_end, period_key, accounting_basis, filing_status, filed_at)
		 VALUES ($1,$2,'2026-03-01','2026-05-31','2026-05-31','invoice','marked_as_filed', now())`, org, user); err != nil {
		t.Fatalf("seed filed vat_return: %v", err)
	}

	t.Run("editing an explanation dated in a filed period → 409", func(t *testing.T) {
		_, err := svc.UpdateExplanation(ctx, userID, orgID, acc.ID, txnIn, explIn,
			banking.CreateExplanationRequest{Type: "PAYMENT", Amount: "100.00", CategoryID: ptr(catID)})
		assertAppCode(t, err, kernel.ErrCodeConflict)
	})

	t.Run("deleting an explanation dated in a filed period → 409", func(t *testing.T) {
		_, err := svc.DeleteExplanation(ctx, userID, orgID, acc.ID, txnIn, explIn)
		assertAppCode(t, err, kernel.ErrCodeConflict)
	})

	t.Run("creating a new explanation dated in a filed period → 409", func(t *testing.T) {
		txn := newTxnOn("2026-04-20", -12000) // £120 line, fully explained by the £120 payment
		_, err := svc.CreateExplanation(ctx, userID, orgID, acc.ID, txn, payment)
		assertAppCode(t, err, kernel.ErrCodeConflict)
	})

	t.Run("moving an explanation INTO a filed period → 409", func(t *testing.T) {
		_, err := svc.UpdateExplanation(ctx, userID, orgID, acc.ID, txnOut, explOut,
			banking.CreateExplanationRequest{Type: "PAYMENT", Amount: "120.00", CategoryID: ptr(catID), DatedOn: ptr("2026-04-12")})
		assertAppCode(t, err, kernel.ErrCodeConflict)
	})

	t.Run("an explanation OUTSIDE every filed period is unaffected", func(t *testing.T) {
		if _, err := svc.UpdateExplanation(ctx, userID, orgID, acc.ID, txnOut, explOut,
			banking.CreateExplanationRequest{Type: "PAYMENT", Amount: "90.00", CategoryID: ptr(catID)}); err != nil {
			t.Errorf("editing an explanation outside any filed period should succeed, got: %v", err)
		}
		if _, err := svc.DeleteExplanation(ctx, userID, orgID, acc.ID, txnOut, explOut); err != nil {
			t.Errorf("deleting an explanation outside any filed period should succeed, got: %v", err)
		}
	})
}
