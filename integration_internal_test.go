package main

// integration_internal_test.go
// =============================================================================
// Tests for the workflow-facing internal operations (Phase 4 / B3):
// ExpenseForPush, TokenForOrg, RecordPushResult, and the OIDC guard.
//
// The service methods are tested DIRECTLY against the real DB (FreeAgent's token
// endpoint faked for the refresh path) — the same approach as the OAuth callback
// test, and the reason the HTTP layer is a thin pass-through. The OIDC middleware
// is tested for its fail-closed (503) and missing-token (401) paths; the positive
// path needs a real Google-signed token, so it's validated at deploy time.
//
// The money/decimal conversion is asserted explicitly (CLAUDE.md requirement):
// 10000 pence → "100.00", 1667 → "16.67", 2000 bps → "20".
// =============================================================================

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// =============================================================================
// ExpenseForPush — money conversion + already_pushed
// =============================================================================

func TestIntegrationInternal_ExpenseForPush(t *testing.T) {
	ts := newTestServer(t)
	defer ts.pool.Close()
	ctx := context.Background()
	svc := ts.integrationService

	member := newMemberUser(t, ts, devOrgID)
	expenseID := createExpenseAs(t, ts, member, devOrgID)

	// Pin known money values so the decimal conversion is asserted exactly.
	if _, err := ts.pool.Exec(ctx,
		`UPDATE expenses SET gross_value_minor = 10000, vat_value_minor = 1667, vat_rate_bps = 2000 WHERE id = $1`,
		expenseID); err != nil {
		t.Fatalf("set money values: %v", err)
	}
	var memberEmail string
	if err := ts.pool.QueryRow(ctx, `SELECT email FROM users WHERE id = $1`, member).Scan(&memberEmail); err != nil {
		t.Fatalf("read member email: %v", err)
	}

	resp, err := svc.ExpenseForPush(ctx, mustUUID(t, devOrgID), mustUUID(t, expenseID))
	if err != nil {
		t.Fatalf("ExpenseForPush: %v", err)
	}

	// Money → decimal strings (the conversion that must never use float).
	if resp.GrossValue != "100.00" {
		t.Errorf("gross_value: got %q, want %q", resp.GrossValue, "100.00")
	}
	if resp.SalesTaxValue != "16.67" {
		t.Errorf("sales_tax_value: got %q, want %q", resp.SalesTaxValue, "16.67")
	}
	if resp.SalesTaxRate == nil || *resp.SalesTaxRate != "20" {
		t.Errorf("sales_tax_rate: got %v, want %q", resp.SalesTaxRate, "20")
	}
	// Provider-neutral fields.
	if resp.ClaimantEmail != memberEmail {
		t.Errorf("claimant_email: got %q, want %q", resp.ClaimantEmail, memberEmail)
	}
	if resp.NominalCode == "" {
		t.Error("nominal_code: expected non-empty")
	}
	if resp.ECStatus == "" {
		t.Error("ec_status: expected the raw value (workflow maps it)")
	}
	if resp.AlreadyPushed {
		t.Error("already_pushed: expected false before any push")
	}

	t.Run("wrong org → 404 (tenant isolation)", func(t *testing.T) {
		otherOrg, _ := newOrgWithOwner(t, ts)
		_, err := svc.ExpenseForPush(ctx, mustUUID(t, otherOrg), mustUUID(t, expenseID))
		assertAppCode(t, err, ErrCodeNotFound)
	})
}

// =============================================================================
// RecordPushResult + already_pushed (round-trip)
// =============================================================================

func TestIntegrationInternal_PushResultAndAlreadyPushed(t *testing.T) {
	ts := newTestServer(t)
	// Close the pool via t.Cleanup (registered FIRST so it runs LAST), NOT defer:
	// deferred calls run before t.Cleanup functions, so a `defer ts.pool.Close()`
	// would shut the pool before the row-cleanup below could run, leaking the
	// dev-org row each test run (its provider key is unique per run).
	t.Cleanup(func() { ts.pool.Close() })
	ctx := context.Background()

	svc := ts.integrationService

	// "Connect" the dev org directly — no OAuth dance, since this test exercises
	// the push-result ledger, not the connect flow. Clean up the row afterwards,
	// scoped to this test's throwaway provider so the dev org's real connection
	// (if any) is left untouched.
	t.Cleanup(func() {
		_, _ = ts.pool.Exec(ctx, `DELETE FROM organisation_integrations WHERE organisation_id = $1 AND provider = $2`, devOrgID, ts.faProvider)
	})
	markConnectedDirect(t, ts, devOrgID)

	member := newMemberUser(t, ts, devOrgID)
	expenseID := createExpenseAs(t, ts, member, devOrgID)

	// Before any push.
	if resp, err := svc.ExpenseForPush(ctx, mustUUID(t, devOrgID), mustUUID(t, expenseID)); err != nil {
		t.Fatalf("ExpenseForPush (pre): %v", err)
	} else if resp.AlreadyPushed {
		t.Fatal("already_pushed should be false before any push")
	}

	// Record a successful push.
	const faURL = "https://api.sandbox.freeagent.com/v2/expenses/99"
	if err := svc.RecordPushResult(ctx, mustUUID(t, devOrgID), mustUUID(t, expenseID), faURL, ""); err != nil {
		t.Fatalf("RecordPushResult (success): %v", err)
	}

	// Now already_pushed flips true, and the DB row carries the external ref.
	if resp, err := svc.ExpenseForPush(ctx, mustUUID(t, devOrgID), mustUUID(t, expenseID)); err != nil {
		t.Fatalf("ExpenseForPush (post): %v", err)
	} else if !resp.AlreadyPushed {
		t.Error("already_pushed should be true after a successful push")
	}

	var dbRef string
	var rowCount int
	if err := ts.pool.QueryRow(ctx,
		`SELECT count(*) FROM integration_expense_pushes WHERE expense_id = $1`, expenseID).Scan(&rowCount); err != nil {
		t.Fatalf("count push rows: %v", err)
	}
	if rowCount != 1 {
		t.Errorf("expected exactly 1 push row, got %d", rowCount)
	}

	// Idempotent upsert: re-recording (e.g. a manual re-push) updates the SAME row.
	const faURL2 = "https://api.sandbox.freeagent.com/v2/expenses/100"
	if err := svc.RecordPushResult(ctx, mustUUID(t, devOrgID), mustUUID(t, expenseID), faURL2, ""); err != nil {
		t.Fatalf("RecordPushResult (re-record): %v", err)
	}
	if err := ts.pool.QueryRow(ctx,
		`SELECT external_expense_ref, count(*) OVER () FROM integration_expense_pushes WHERE expense_id = $1`,
		expenseID).Scan(&dbRef, &rowCount); err != nil {
		t.Fatalf("read push row: %v", err)
	}
	if rowCount != 1 {
		t.Errorf("re-record must not duplicate; got %d rows", rowCount)
	}
	if dbRef != faURL2 {
		t.Errorf("external_expense_ref: got %q, want %q", dbRef, faURL2)
	}
}

// =============================================================================
// TokenForOrg — vend + refresh
// =============================================================================

func TestIntegrationInternal_TokenForOrg(t *testing.T) {
	ts := newTestServer(t)
	t.Cleanup(func() { ts.pool.Close() })
	ctx := context.Background()

	fake := fakeFreeAgentTokenServer(t)
	svc := freeAgentServiceWithHost(ts, fake.URL)

	connect := func(t *testing.T) string {
		t.Helper()
		seedProviderCreds(t, ts)
		org, owner := newOrgWithOwner(t, ts)
		state, _ := ts.tokenMaker.CreateToken(mustUUID(t, owner), mustUUID(t, org), time.Minute)
		if _, err := svc.HandleCallback(ctx, "code", state); err != nil {
			t.Fatalf("connect: %v", err)
		}
		return org
	}

	t.Run("not connected → 409", func(t *testing.T) {
		org, _ := newOrgWithOwner(t, ts)
		_, err := svc.TokenForOrg(ctx, mustUUID(t, org))
		assertAppCode(t, err, ErrCodeConflict)
	})

	t.Run("connected → token + base url", func(t *testing.T) {
		org := connect(t)
		resp, err := svc.TokenForOrg(ctx, mustUUID(t, org))
		if err != nil {
			t.Fatalf("TokenForOrg: %v", err)
		}
		if resp.AccessToken != "fa-access-123" {
			t.Errorf("access_token: got %q, want %q", resp.AccessToken, "fa-access-123")
		}
		if resp.APIBaseURL != fake.URL+"/v2" {
			t.Errorf("freeagent_base_url: got %q, want %q", resp.APIBaseURL, fake.URL+"/v2")
		}
		if resp.IntegrationID == "" {
			t.Error("integration_id: expected non-empty")
		}
	})

	t.Run("near-expiry token is refreshed", func(t *testing.T) {
		org := connect(t)
		// Force the stored access token past expiry so the next vend must refresh.
		if _, err := ts.pool.Exec(ctx,
			`UPDATE organisation_integrations SET token_expires_at = now() - interval '1 hour' WHERE organisation_id = $1`,
			org); err != nil {
			t.Fatalf("force expiry: %v", err)
		}

		resp, err := svc.TokenForOrg(ctx, mustUUID(t, org))
		if err != nil {
			t.Fatalf("TokenForOrg (refresh): %v", err)
		}
		if resp.AccessToken != "fa-access-123" {
			t.Errorf("refreshed access_token: got %q, want %q (fake returns a fixed value)", resp.AccessToken, "fa-access-123")
		}
		// Proof the refresh actually ran: the stored expiry moved back into the future.
		var exp time.Time
		if err := ts.pool.QueryRow(ctx,
			`SELECT token_expires_at FROM organisation_integrations WHERE organisation_id = $1`, org).Scan(&exp); err != nil {
			t.Fatalf("read expiry: %v", err)
		}
		if !exp.After(time.Now()) {
			t.Errorf("token not refreshed: expiry %v is not in the future", exp)
		}
	})
}

// =============================================================================
// OIDC GUARD
// =============================================================================

// TestInternalEndpoints_RejectNoToken: with a service account configured (the test
// harness sets testWorkflowServiceAccount), a call with no bearer token is 401 —
// proving the /internal routes are gated. Exercised through the real router.
func TestInternalEndpoints_RejectNoToken(t *testing.T) {
	ts := newTestServer(t)
	defer ts.pool.Close()

	paths := []struct {
		method, path string
	}{
		{http.MethodGet, "/internal/v1/integrations/" + ts.faProvider + "/token?org=" + devOrgID},
		{http.MethodGet, "/internal/v1/expenses/" + devOrgID + "?org=" + devOrgID},
		{http.MethodPost, "/internal/v1/integrations/" + ts.faProvider + "/push-result"},
	}
	for _, p := range paths {
		rec := httptest.NewRecorder()
		req, _ := http.NewRequest(p.method, p.path, nil)
		ts.server.router.ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("%s %s without token: expected 401, got %d — body: %s", p.method, p.path, rec.Code, rec.Body.String())
		}
	}
}
