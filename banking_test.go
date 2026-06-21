package main

// banking_test.go
// =============================================================================
// Data-layer tests for the banking module (bank_accounts + bank_transactions),
// exercising the sqlc-generated banking.Queries directly against real PostgreSQL
// — the same "real DB, not mocks" approach as the other domain tests. There is no
// service/HTTP layer yet (that's the next increment), so these prove the SCHEMA
// and QUERIES: the derived balance, the signed-amount sum, the primary-account
// and feed-dedupe unique indexes, soft delete, and multi-tenant scoping.
//
// Each test runs under a FRESH ephemeral org (newOrgWithOwner) so list/balance
// assertions are clean and never collide with the dev seed (db/seeds/bank_accounts.sql).
// Skips without DATABASE_URL, like every other DB test.
// =============================================================================

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/operationfb/accounting-saas/db/banking"
)

// =============================================================================
// HELPERS
// =============================================================================

func pgText(s string) pgtype.Text { return pgtype.Text{String: s, Valid: true} }

func mkdate(y int, m time.Month, d int) pgtype.Date {
	return pgtype.Date{Time: time.Date(y, m, d, 0, 0, 0, 0, time.UTC), Valid: true}
}

// isUniqueViolation (SQLSTATE 23505) — the primary-account and feed-dedupe
// indexes raise it — is shared from email_inbox_service.go (same package).

// createBankAccount inserts an account via the generated query with sensible
// defaults (override via mutate) and registers cleanup that hard-deletes the
// account and its transactions afterwards, keeping the shared dev DB clean.
func createBankAccount(t *testing.T, ts *testServer, q *banking.Queries, orgID, userID string, mutate func(*banking.CreateBankAccountParams)) banking.BankAccount {
	t.Helper()
	p := banking.CreateBankAccountParams{
		OrganisationID:    mustUUID(t, orgID),
		CreatedByUserID:   mustUUID(t, userID),
		Name:              "Test Bank Account",
		Currency:          "GBP",
		Status:            "active",
		ShowOnInvoices:    true,
		GuessExplanations: true,
	}
	if mutate != nil {
		mutate(&p)
	}
	acc, err := q.CreateBankAccount(context.Background(), p)
	if err != nil {
		t.Fatalf("CreateBankAccount: %v", err)
	}
	t.Cleanup(func() {
		ctx := context.Background()
		// transactions first (FK to bank_accounts), then the account itself
		_, _ = ts.pool.Exec(ctx, `DELETE FROM bank_transactions WHERE bank_account_id = $1`, acc.ID)
		_, _ = ts.pool.Exec(ctx, `DELETE FROM bank_accounts WHERE id = $1`, acc.ID)
	})
	return acc
}

// createBankTxn inserts one transaction via the generated query. Cleanup is
// handled by the owning account's cleanup (DELETE ... WHERE bank_account_id),
// so this registers none of its own.
func createBankTxn(t *testing.T, q *banking.Queries, orgID string, accID uuid.UUID, dated pgtype.Date, amount int64, mutate func(*banking.CreateBankTransactionParams)) banking.BankTransaction {
	t.Helper()
	p := banking.CreateBankTransactionParams{
		OrganisationID: mustUUID(t, orgID),
		BankAccountID:  accID,
		DatedOn:        dated,
		AmountMinor:    amount,
		Status:         "unexplained",
		Source:         "manual",
	}
	if mutate != nil {
		mutate(&p)
	}
	txn, err := q.CreateBankTransaction(context.Background(), p)
	if err != nil {
		t.Fatalf("CreateBankTransaction: %v", err)
	}
	return txn
}

// =============================================================================
// TESTS
// =============================================================================

func TestBankingDataLayer(t *testing.T) {
	ts := newTestServer(t)
	defer ts.pool.Close()
	q := banking.New(ts.pool)
	ctx := context.Background()

	t.Run("create round-trips; balance holds values above int32", func(t *testing.T) {
		org, user := newOrgWithOwner(t, ts)
		// £90,000,000 in pence. int32 maxes at ~£21.4m, so this only fits int64 —
		// proving the BIGINT/int64 money path (the reason these columns aren't INTEGER).
		const big = int64(9_000_000_000)
		acc := createBankAccount(t, ts, q, org, user, func(p *banking.CreateBankAccountParams) {
			p.Name = "Acme Current"
			p.OpeningBalanceMinor = big
			p.BankName = pgText("NatWest")
			p.SortCode = pgText("601441")
			p.AccountNumber = pgText("66686210")
		})
		if acc.OpeningBalanceMinor != big {
			t.Errorf("opening balance round-trip: got %d, want %d", acc.OpeningBalanceMinor, big)
		}
		if acc.Name != "Acme Current" || acc.BankName.String != "NatWest" || acc.SortCode.String != "601441" {
			t.Errorf("field round-trip mismatch: name=%q bank=%q sort=%q", acc.Name, acc.BankName.String, acc.SortCode.String)
		}
		// With no transactions, the derived current balance equals the opening balance.
		got, err := q.GetBankAccount(ctx, banking.GetBankAccountParams{ID: acc.ID, OrganisationID: mustUUID(t, org)})
		if err != nil {
			t.Fatalf("GetBankAccount: %v", err)
		}
		if got.CurrentBalanceMinor != big {
			t.Errorf("fresh account balance: got %d, want %d", got.CurrentBalanceMinor, big)
		}
	})

	t.Run("derived balance = opening + signed sum of transactions", func(t *testing.T) {
		org, user := newOrgWithOwner(t, ts)
		acc := createBankAccount(t, ts, q, org, user, func(p *banking.CreateBankAccountParams) {
			p.OpeningBalanceMinor = 100_000 // £1,000.00
		})
		createBankTxn(t, q, org, acc.ID, mkdate(2026, 6, 1), 50_000, nil)  // +£500.00 in
		createBankTxn(t, q, org, acc.ID, mkdate(2026, 6, 2), -20_000, nil) // -£200.00 out
		const want = int64(100_000 + 50_000 - 20_000)                      // £1,300.00

		got, err := q.GetBankAccount(ctx, banking.GetBankAccountParams{ID: acc.ID, OrganisationID: mustUUID(t, org)})
		if err != nil {
			t.Fatalf("GetBankAccount: %v", err)
		}
		if got.CurrentBalanceMinor != want {
			t.Errorf("GetBankAccount balance: got %d, want %d", got.CurrentBalanceMinor, want)
		}
		// ListBankAccounts must report the same derived balance.
		list, err := q.ListBankAccounts(ctx, mustUUID(t, org))
		if err != nil {
			t.Fatalf("ListBankAccounts: %v", err)
		}
		if len(list) != 1 {
			t.Fatalf("list: got %d accounts, want 1", len(list))
		}
		if list[0].CurrentBalanceMinor != want {
			t.Errorf("ListBankAccounts balance: got %d, want %d", list[0].CurrentBalanceMinor, want)
		}
	})

	t.Run("a soft-deleted transaction drops out of the derived balance", func(t *testing.T) {
		org, user := newOrgWithOwner(t, ts)
		acc := createBankAccount(t, ts, q, org, user, func(p *banking.CreateBankAccountParams) { p.OpeningBalanceMinor = 0 })
		createBankTxn(t, q, org, acc.ID, mkdate(2026, 6, 1), 7_000, nil) // kept
		gone := createBankTxn(t, q, org, acc.ID, mkdate(2026, 6, 2), 3_000, nil)

		if err := q.SoftDeleteBankTransaction(ctx, banking.SoftDeleteBankTransactionParams{ID: gone.ID, OrganisationID: mustUUID(t, org)}); err != nil {
			t.Fatalf("SoftDeleteBankTransaction: %v", err)
		}
		got, err := q.GetBankAccount(ctx, banking.GetBankAccountParams{ID: acc.ID, OrganisationID: mustUUID(t, org)})
		if err != nil {
			t.Fatalf("GetBankAccount: %v", err)
		}
		// Only the kept £70.00 should count — proves the LEFT JOIN's deleted_at filter
		// lives in the ON clause (not WHERE), so it excludes the line without dropping the account.
		if got.CurrentBalanceMinor != 7_000 {
			t.Errorf("balance after soft-deleting a txn: got %d, want 7000", got.CurrentBalanceMinor)
		}
	})

	t.Run("at most one primary account per org", func(t *testing.T) {
		org, user := newOrgWithOwner(t, ts)
		primary := func(p *banking.CreateBankAccountParams) { p.IsPrimary = true }
		createBankAccount(t, ts, q, org, user, primary) // first primary: ok

		// A second live primary for the same org violates idx_bank_accounts_one_primary.
		_, err := q.CreateBankAccount(ctx, banking.CreateBankAccountParams{
			OrganisationID: mustUUID(t, org), CreatedByUserID: mustUUID(t, user),
			Name: "Second", Currency: "GBP", Status: "active", IsPrimary: true,
			ShowOnInvoices: true, GuessExplanations: true,
		})
		if !isUniqueViolation(err) {
			t.Fatalf("second primary: want unique violation, got %v", err)
		}

		// After clearing the org's primary, a new primary is allowed (the service's
		// unset-then-set flow). The helper's t.Fatalf would catch any error here.
		if err := q.UnsetPrimaryBankAccounts(ctx, mustUUID(t, org)); err != nil {
			t.Fatalf("UnsetPrimaryBankAccounts: %v", err)
		}
		createBankAccount(t, ts, q, org, user, primary)
	})

	t.Run("a soft-deleted account disappears from get and list", func(t *testing.T) {
		org, user := newOrgWithOwner(t, ts)
		acc := createBankAccount(t, ts, q, org, user, nil)
		if err := q.SoftDeleteBankAccount(ctx, banking.SoftDeleteBankAccountParams{ID: acc.ID, OrganisationID: mustUUID(t, org)}); err != nil {
			t.Fatalf("SoftDeleteBankAccount: %v", err)
		}
		if _, err := q.GetBankAccount(ctx, banking.GetBankAccountParams{ID: acc.ID, OrganisationID: mustUUID(t, org)}); !errors.Is(err, pgx.ErrNoRows) {
			t.Errorf("GetBankAccount after soft delete: want ErrNoRows, got %v", err)
		}
		list, err := q.ListBankAccounts(ctx, mustUUID(t, org))
		if err != nil {
			t.Fatalf("ListBankAccounts: %v", err)
		}
		if len(list) != 0 {
			t.Errorf("ListBankAccounts after soft delete: want 0, got %d", len(list))
		}
	})

	t.Run("queries are organisation-scoped", func(t *testing.T) {
		orgA, userA := newOrgWithOwner(t, ts)
		orgB, _ := newOrgWithOwner(t, ts)
		acc := createBankAccount(t, ts, q, orgA, userA, nil)
		txn := createBankTxn(t, q, orgA, acc.ID, mkdate(2026, 6, 1), 1_000, nil)

		// Org B must not be able to read org A's account or transaction by id.
		if _, err := q.GetBankAccount(ctx, banking.GetBankAccountParams{ID: acc.ID, OrganisationID: mustUUID(t, orgB)}); !errors.Is(err, pgx.ErrNoRows) {
			t.Errorf("cross-tenant GetBankAccount: want ErrNoRows, got %v", err)
		}
		if _, err := q.GetBankTransaction(ctx, banking.GetBankTransactionParams{ID: txn.ID, OrganisationID: mustUUID(t, orgB)}); !errors.Is(err, pgx.ErrNoRows) {
			t.Errorf("cross-tenant GetBankTransaction: want ErrNoRows, got %v", err)
		}
		// And org B's list is empty (it owns nothing).
		if list, err := q.ListBankAccounts(ctx, mustUUID(t, orgB)); err != nil || len(list) != 0 {
			t.Errorf("org B ListBankAccounts: got %d (err %v), want 0", len(list), err)
		}
	})

	t.Run("feed external_id dedupes per account; manual NULLs don't", func(t *testing.T) {
		org, user := newOrgWithOwner(t, ts)
		acc := createBankAccount(t, ts, q, org, user, nil)

		feed := func(p *banking.CreateBankTransactionParams) { p.Source = "feed"; p.ExternalID = pgText("txn-abc") }
		createBankTxn(t, q, org, acc.ID, mkdate(2026, 6, 1), -1_000, feed) // first feed line: ok

		// The dedupe lookup the future ingestion uses finds it.
		found, err := q.GetBankTransactionByExternalID(ctx, banking.GetBankTransactionByExternalIDParams{BankAccountID: acc.ID, ExternalID: pgText("txn-abc")})
		if err != nil || found.ExternalID.String != "txn-abc" {
			t.Errorf("GetBankTransactionByExternalID: got %q (err %v), want \"txn-abc\"", found.ExternalID.String, err)
		}

		// Same (account, external_id) again → unique violation (idx_bank_transactions_external).
		_, err = q.CreateBankTransaction(ctx, banking.CreateBankTransactionParams{
			OrganisationID: mustUUID(t, org), BankAccountID: acc.ID, DatedOn: mkdate(2026, 6, 2),
			AmountMinor: -1_000, Status: "unexplained", Source: "feed", ExternalID: pgText("txn-abc"),
		})
		if !isUniqueViolation(err) {
			t.Fatalf("duplicate external_id: want unique violation, got %v", err)
		}

		// But two manual rows with NULL external_id coexist (the index is partial).
		createBankTxn(t, q, org, acc.ID, mkdate(2026, 6, 3), -500, nil)
		createBankTxn(t, q, org, acc.ID, mkdate(2026, 6, 3), -500, nil)
	})

	t.Run("transactions list newest-first and paginated", func(t *testing.T) {
		org, user := newOrgWithOwner(t, ts)
		acc := createBankAccount(t, ts, q, org, user, nil)
		createBankTxn(t, q, org, acc.ID, mkdate(2026, 6, 1), 100, nil)
		createBankTxn(t, q, org, acc.ID, mkdate(2026, 6, 2), 200, nil)
		createBankTxn(t, q, org, acc.ID, mkdate(2026, 6, 3), 300, nil)

		page1, err := q.ListBankTransactions(ctx, banking.ListBankTransactionsParams{
			OrganisationID: mustUUID(t, org), BankAccountID: acc.ID, Limit: 2, Offset: 0,
		})
		if err != nil {
			t.Fatalf("ListBankTransactions page1: %v", err)
		}
		if len(page1) != 2 {
			t.Fatalf("page1: got %d rows, want 2", len(page1))
		}
		if page1[0].AmountMinor != 300 || page1[1].AmountMinor != 200 {
			t.Errorf("page1 newest-first: got [%d, %d], want [300, 200]", page1[0].AmountMinor, page1[1].AmountMinor)
		}
		page2, err := q.ListBankTransactions(ctx, banking.ListBankTransactionsParams{
			OrganisationID: mustUUID(t, org), BankAccountID: acc.ID, Limit: 2, Offset: 2,
		})
		if err != nil {
			t.Fatalf("ListBankTransactions page2: %v", err)
		}
		if len(page2) != 1 || page2[0].AmountMinor != 100 {
			t.Errorf("page2: got %d rows, want 1 with amount 100", len(page2))
		}
	})
}
