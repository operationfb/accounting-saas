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
	defer ts.pool.Close()
	ctx := context.Background()
	svc := ts.bankingService

	t.Run("create round-trips money + fields and applies defaults", func(t *testing.T) {
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
		org, _ := newOrgWithOwner(t, ts)
		member := addMember(t, ts, org, "member")
		_, err := svc.CreateBankAccount(ctx, mustUUID(t, member), mustUUID(t, org), bankReq("Nope", nil))
		assertAppCode(t, err, kernel.ErrCodeForbidden)
	})

	t.Run("cannot read another org's account (404)", func(t *testing.T) {
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
		org, user := newOrgWithOwner(t, ts)
		_, err := svc.CreateBankAccount(ctx, mustUUID(t, user), mustUUID(t, org),
			bankReq("Bad", func(r *banking.CreateBankAccountRequest) { r.OpeningBalance = "not-a-number" }))
		assertAppCode(t, err, kernel.ErrCodeValidation)
	})

	t.Run("ListTransactions returns the statement with a running balance", func(t *testing.T) {
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

	importCSV := func(t *testing.T, userID, orgID, accID, csv string) (*banking.StatementImportResponse, error) {
		t.Helper()
		return svc.ImportStatement(ctx, mustUUID(t, userID), mustUUID(t, orgID), accID, strings.NewReader(csv))
	}

	t.Run("statement import: valid CSV creates statement lines + is idempotent", func(t *testing.T) {
		org, user := newOrgWithOwner(t, ts)
		acc, err := svc.CreateBankAccount(ctx, mustUUID(t, user), mustUUID(t, org),
			bankReq("Import", func(r *banking.CreateBankAccountRequest) { r.OpeningBalance = "1000.00" }))
		if err != nil {
			t.Fatalf("create account: %v", err)
		}
		cleanupBankAccount(t, ts, acc.ID)

		csv := "date,description,money_in,money_out\n" +
			"11/06/2026,Salary,16413.59,\n" +
			"12/06/2026,Coffee,,10.08\n"
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
		org, user := newOrgWithOwner(t, ts)
		acc, err := svc.CreateBankAccount(ctx, mustUUID(t, user), mustUUID(t, org), bankReq("Dup", nil))
		if err != nil {
			t.Fatalf("create account: %v", err)
		}
		cleanupBankAccount(t, ts, acc.ID)
		csv := "date,description,money_in,money_out\n" +
			"11/06/2026,Coffee,,5.00\n" +
			"11/06/2026,Coffee,,5.00\n" // byte-identical line
		res, err := importCSV(t, user, org, acc.ID, csv)
		if err != nil {
			t.Fatalf("import: %v", err)
		}
		if res.Imported != 2 {
			t.Errorf("identical within-file lines should both import: got imported=%d, want 2", res.Imported)
		}
	})

	t.Run("statement import: an invalid CSV is a 422 and imports nothing", func(t *testing.T) {
		org, user := newOrgWithOwner(t, ts)
		acc, err := svc.CreateBankAccount(ctx, mustUUID(t, user), mustUUID(t, org), bankReq("BadCSV", nil))
		if err != nil {
			t.Fatalf("create account: %v", err)
		}
		cleanupBankAccount(t, ts, acc.ID)
		for _, bad := range []string{
			"description,money_in,money_out\nSalary,10.00,\n",                   // missing date column
			"date,description,money_in,money_out\n11/06/2026,Both,10.00,5.00\n", // both money columns
			"date,description,money_in,money_out\n11/06/2026,Neither,,\n",       // neither money column
			"date,description,money_in,money_out\n2026-06-11,BadDate,10.00,\n",  // wrong date format
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
		org, user := newOrgWithOwner(t, ts)
		acc, err := svc.CreateBankAccount(ctx, mustUUID(t, user), mustUUID(t, org), bankReq("ImportAuthz", nil))
		if err != nil {
			t.Fatalf("create account: %v", err)
		}
		cleanupBankAccount(t, ts, acc.ID)
		csv := "date,description,money_in,money_out\n11/06/2026,X,1.00,\n"

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
}
