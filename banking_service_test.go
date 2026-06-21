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
	"testing"

	"github.com/google/uuid"

	banking "github.com/operationfb/accounting-saas/internal/banking"
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
			assertAppCode(t, err, ErrCodeNotFound)
		}
		if list, _ := svc.ListBankAccounts(ctx, mustUUID(t, user), mustUUID(t, org)); len(list) != 0 {
			t.Errorf("list after delete: want 0, got %d", len(list))
		}
	})

	t.Run("a non-admin member cannot create (403)", func(t *testing.T) {
		org, _ := newOrgWithOwner(t, ts)
		member := addMember(t, ts, org, "member")
		_, err := svc.CreateBankAccount(ctx, mustUUID(t, member), mustUUID(t, org), bankReq("Nope", nil))
		assertAppCode(t, err, ErrCodeForbidden)
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
		assertAppCode(t, err, ErrCodeNotFound)
	})

	t.Run("an invalid opening_balance is a 422", func(t *testing.T) {
		org, user := newOrgWithOwner(t, ts)
		_, err := svc.CreateBankAccount(ctx, mustUUID(t, user), mustUUID(t, org),
			bankReq("Bad", func(r *banking.CreateBankAccountRequest) { r.OpeningBalance = "not-a-number" }))
		assertAppCode(t, err, ErrCodeValidation)
	})
}
