package main

// integration_service_test.go
// =============================================================================
// Integration tests for the FreeAgent OAuth connect + credential/token store
// (the monolith's half of the push integration — Phase 2 / B2).
//
// Like the rest of the suite these hit a REAL PostgreSQL database via the shared
// newTestServer harness and skip when DATABASE_URL is unset. Mutating tests run
// against a throwaway org (newOrgWithOwner) whose owner is an admin, plus a
// non-admin via newMemberUser — both clean up after themselves, and the
// organisation_integrations rows cascade-delete with the org.
//
// FreeAgent itself is the one external service we fake (per the "mock only
// third-party services" rule): the credential/status/connect tests never call it,
// and the callback test points a freeAgentClient at an httptest.Server standing in
// for FreeAgent's token endpoint, so the OAuth code exchange is exercised end to
// end without real network calls.
//
// Coverage: save/read credentials + status; not-configured state; owner/admin-only
// (member & non-member 403, unauth 401); binding validation (400); connect URL
// shape + "save credentials first" (422); the callback happy path (tokens stored,
// connected_at set, success redirect); invalid state + missing code (error
// redirect, no tokens); disconnect (tokens cleared, credentials kept); and
// multi-tenant isolation.
// =============================================================================

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/operationfb/accounting-saas/db/auth"
	integrationsdb "github.com/operationfb/accounting-saas/db/integrations"
)

// =============================================================================
// HTTP TEST HELPERS
// =============================================================================

func putFreeAgentCreds(t *testing.T, ts *testServer, authHeader string, body SaveFreeAgentCredentialsRequest) *httptest.ResponseRecorder {
	t.Helper()
	b, _ := json.Marshal(body)
	rec := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodPut, "/api/v1/integrations/freeagent", bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	if authHeader != "" {
		req.Header.Set("Authorization", authHeader)
	}
	ts.server.router.ServeHTTP(rec, req)
	return rec
}

func getFreeAgentStatus(t *testing.T, ts *testServer, authHeader string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodGet, "/api/v1/integrations/freeagent", nil)
	if authHeader != "" {
		req.Header.Set("Authorization", authHeader)
	}
	ts.server.router.ServeHTTP(rec, req)
	return rec
}

func deleteFreeAgentReq(t *testing.T, ts *testServer, authHeader string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodDelete, "/api/v1/integrations/freeagent", nil)
	if authHeader != "" {
		req.Header.Set("Authorization", authHeader)
	}
	ts.server.router.ServeHTTP(rec, req)
	return rec
}

func getFreeAgentConnect(t *testing.T, ts *testServer, authHeader string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodGet, "/api/v1/freeagent/connect", nil)
	if authHeader != "" {
		req.Header.Set("Authorization", authHeader)
	}
	ts.server.router.ServeHTTP(rec, req)
	return rec
}

func decodeFAStatus(t *testing.T, body []byte) FreeAgentStatusResponse {
	t.Helper()
	var resp struct {
		Integration FreeAgentStatusResponse `json:"integration"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		t.Fatalf("decode integration status: %v — body: %s", err, string(body))
	}
	return resp.Integration
}

func getFreeAgentPushStatus(t *testing.T, ts *testServer, authHeader, expenseID string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodGet, "/api/v1/integrations/freeagent/expenses/"+expenseID+"/push", nil)
	if authHeader != "" {
		req.Header.Set("Authorization", authHeader)
	}
	ts.server.router.ServeHTTP(rec, req)
	return rec
}

func decodeFAPush(t *testing.T, body []byte) FreeAgentPushStatusResponse {
	t.Helper()
	var resp struct {
		Push FreeAgentPushStatusResponse `json:"push"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		t.Fatalf("decode push status: %v — body: %s", err, string(body))
	}
	return resp.Push
}

// fakeFreeAgentTokenServer stands in for FreeAgent's /v2/token_endpoint, returning
// a fixed token response for both code-exchange and refresh.
func fakeFreeAgentTokenServer(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != freeAgentTokenPath {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"fa-access-123","refresh_token":"fa-refresh-456","token_type":"bearer","expires_in":3600,"refresh_token_expires_in":631151957}`))
	}))
	t.Cleanup(srv.Close)
	return srv
}

// freeAgentServiceWithHost builds an IntegrationService whose FreeAgent client
// points at host (an httptest.Server URL), so token calls hit the fake instead of
// real FreeAgent. It shares the harness's pool + maker.
func freeAgentServiceWithHost(ts *testServer, host string) *IntegrationService {
	client := &freeAgentClient{host: host, httpClient: &http.Client{Timeout: 5 * time.Second}}
	return NewIntegrationService(integrationsdb.New(ts.pool), auth.New(ts.pool), client, ts.tokenMaker, "http://api.test", testAppBaseURL)
}

// =============================================================================
// CREDENTIALS + STATUS
// =============================================================================

func TestFreeAgentIntegration_CredentialsAndStatus(t *testing.T) {
	ts := newTestServer(t)
	defer ts.pool.Close()

	t.Run("owner saves credentials → 200, has_credentials, not connected; GET reflects", func(t *testing.T) {
		org, owner := newOrgWithOwner(t, ts)
		authHeader := bearer(t, ts, owner, org)

		rec := putFreeAgentCreds(t, ts, authHeader, SaveFreeAgentCredentialsRequest{ClientID: "cid-abc", ClientSecret: "csecret-xyz"})
		if rec.Code != http.StatusOK {
			t.Fatalf("PUT credentials: expected 200, got %d — body: %s", rec.Code, rec.Body.String())
		}
		got := decodeFAStatus(t, rec.Body.Bytes())
		if !got.HasCredentials {
			t.Error("has_credentials: expected true after saving")
		}
		if got.Connected {
			t.Error("connected: expected false (saving credentials does not connect)")
		}

		// Re-GET reflects the same state.
		getRec := getFreeAgentStatus(t, ts, authHeader)
		if getRec.Code != http.StatusOK {
			t.Fatalf("GET status: expected 200, got %d — body: %s", getRec.Code, getRec.Body.String())
		}
		if reread := decodeFAStatus(t, getRec.Body.Bytes()); !reread.HasCredentials || reread.Connected {
			t.Errorf("re-GET status mismatch: %+v", reread)
		}

		// The secret is stored but NEVER returned in the status JSON.
		if strings.Contains(rec.Body.String(), "csecret-xyz") {
			t.Error("status response leaked the client_secret")
		}
	})

	t.Run("not configured → has_credentials false, not connected", func(t *testing.T) {
		org, owner := newOrgWithOwner(t, ts)
		rec := getFreeAgentStatus(t, ts, bearer(t, ts, owner, org))
		if rec.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d — body: %s", rec.Code, rec.Body.String())
		}
		if got := decodeFAStatus(t, rec.Body.Bytes()); got.HasCredentials || got.Connected {
			t.Errorf("unconfigured org: expected all-false status, got %+v", got)
		}
	})

	t.Run("non-admin member cannot save/read → 403", func(t *testing.T) {
		org, _ := newOrgWithOwner(t, ts)
		member := newMemberUser(t, ts, org)
		authHeader := bearer(t, ts, member, org)

		if rec := putFreeAgentCreds(t, ts, authHeader, SaveFreeAgentCredentialsRequest{ClientID: "x", ClientSecret: "y"}); rec.Code != http.StatusForbidden {
			t.Errorf("member PUT: expected 403, got %d — body: %s", rec.Code, rec.Body.String())
		}
		if rec := getFreeAgentStatus(t, ts, authHeader); rec.Code != http.StatusForbidden {
			t.Errorf("member GET: expected 403, got %d — body: %s", rec.Code, rec.Body.String())
		}
	})

	t.Run("non-member cannot read → 403", func(t *testing.T) {
		org, _ := newOrgWithOwner(t, ts)
		if rec := getFreeAgentStatus(t, ts, bearer(t, ts, devUserID, org)); rec.Code != http.StatusForbidden {
			t.Errorf("non-member GET: expected 403, got %d — body: %s", rec.Code, rec.Body.String())
		}
	})

	t.Run("requires auth → 401", func(t *testing.T) {
		if rec := getFreeAgentStatus(t, ts, ""); rec.Code != http.StatusUnauthorized {
			t.Errorf("expected 401, got %d — body: %s", rec.Code, rec.Body.String())
		}
	})

	t.Run("missing client_id/secret → 400 binding", func(t *testing.T) {
		org, owner := newOrgWithOwner(t, ts)
		rec := putFreeAgentCreds(t, ts, bearer(t, ts, owner, org), SaveFreeAgentCredentialsRequest{ClientID: "only-id"})
		if rec.Code != http.StatusBadRequest {
			t.Errorf("expected 400, got %d — body: %s", rec.Code, rec.Body.String())
		}
	})
}

// =============================================================================
// CONNECT
// =============================================================================

func TestFreeAgentIntegration_Connect(t *testing.T) {
	ts := newTestServer(t)
	defer ts.pool.Close()

	t.Run("owner with credentials → authorize_url with the OAuth params", func(t *testing.T) {
		org, owner := newOrgWithOwner(t, ts)
		authHeader := bearer(t, ts, owner, org)
		if rec := putFreeAgentCreds(t, ts, authHeader, SaveFreeAgentCredentialsRequest{ClientID: "cid-connect", ClientSecret: "sec"}); rec.Code != http.StatusOK {
			t.Fatalf("save creds: %d — %s", rec.Code, rec.Body.String())
		}

		rec := getFreeAgentConnect(t, ts, authHeader)
		if rec.Code != http.StatusOK {
			t.Fatalf("connect: expected 200, got %d — body: %s", rec.Code, rec.Body.String())
		}
		var resp struct {
			AuthorizeURL string `json:"authorize_url"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
			t.Fatalf("decode authorize_url: %v", err)
		}
		for _, want := range []string{"approve_app", "response_type=code", "client_id=cid-connect", "state=", "redirect_uri="} {
			if !strings.Contains(resp.AuthorizeURL, want) {
				t.Errorf("authorize_url missing %q: %s", want, resp.AuthorizeURL)
			}
		}
	})

	t.Run("no credentials saved → 422", func(t *testing.T) {
		org, owner := newOrgWithOwner(t, ts)
		rec := getFreeAgentConnect(t, ts, bearer(t, ts, owner, org))
		if rec.Code != http.StatusUnprocessableEntity {
			t.Errorf("connect without creds: expected 422, got %d — body: %s", rec.Code, rec.Body.String())
		}
	})

	t.Run("non-admin member → 403", func(t *testing.T) {
		org, _ := newOrgWithOwner(t, ts)
		member := newMemberUser(t, ts, org)
		rec := getFreeAgentConnect(t, ts, bearer(t, ts, member, org))
		if rec.Code != http.StatusForbidden {
			t.Errorf("member connect: expected 403, got %d — body: %s", rec.Code, rec.Body.String())
		}
	})
}

// =============================================================================
// CALLBACK (OAuth code exchange) — FreeAgent token endpoint faked
// =============================================================================

func TestFreeAgentIntegration_Callback(t *testing.T) {
	ts := newTestServer(t)
	t.Cleanup(func() { ts.pool.Close() })

	ctx := context.Background()
	fake := fakeFreeAgentTokenServer(t)
	svc := freeAgentServiceWithHost(ts, fake.URL)

	t.Run("valid state + code → tokens stored, connected, success redirect", func(t *testing.T) {
		org, owner := newOrgWithOwner(t, ts)
		if _, err := svc.SaveCredentials(ctx, mustUUID(t, owner), mustUUID(t, org), SaveFreeAgentCredentialsRequest{ClientID: "cid", ClientSecret: "sec"}); err != nil {
			t.Fatalf("save creds: %v", err)
		}
		state, err := ts.tokenMaker.CreateToken(mustUUID(t, owner), mustUUID(t, org), time.Minute)
		if err != nil {
			t.Fatalf("mint state: %v", err)
		}

		redirectURL, internalErr := svc.HandleCallback(ctx, "auth-code-123", state)
		if internalErr != nil {
			t.Fatalf("HandleCallback internal error: %v", internalErr)
		}
		if !strings.Contains(redirectURL, "freeagent=connected") {
			t.Errorf("expected success redirect, got %q", redirectURL)
		}

		// Tokens persisted + connected_at set — only a real DB proves this.
		var access, refresh string
		var connectedAt time.Time
		if err := ts.pool.QueryRow(ctx,
			`SELECT access_token, refresh_token, connected_at FROM organisation_integrations WHERE organisation_id=$1 AND provider='freeagent'`,
			org).Scan(&access, &refresh, &connectedAt); err != nil {
			t.Fatalf("read integration row: %v", err)
		}
		if access != "fa-access-123" || refresh != "fa-refresh-456" {
			t.Errorf("tokens not stored: access=%q refresh=%q", access, refresh)
		}
		if connectedAt.IsZero() {
			t.Error("connected_at not set")
		}
	})

	t.Run("invalid state → error redirect, no tokens stored", func(t *testing.T) {
		org, owner := newOrgWithOwner(t, ts)
		if _, err := svc.SaveCredentials(ctx, mustUUID(t, owner), mustUUID(t, org), SaveFreeAgentCredentialsRequest{ClientID: "cid", ClientSecret: "sec"}); err != nil {
			t.Fatalf("save creds: %v", err)
		}

		redirectURL, internalErr := svc.HandleCallback(ctx, "code", "not-a-valid-state-token")
		if internalErr != nil {
			t.Fatalf("unexpected internal error: %v", internalErr)
		}
		if !strings.Contains(redirectURL, "freeagent=error") || !strings.Contains(redirectURL, "reason=invalid_state") {
			t.Errorf("expected invalid_state error redirect, got %q", redirectURL)
		}

		// access_token is still NULL (nullable → *string scans nil).
		var access *string
		if err := ts.pool.QueryRow(ctx,
			`SELECT access_token FROM organisation_integrations WHERE organisation_id=$1 AND provider='freeagent'`,
			org).Scan(&access); err != nil {
			t.Fatalf("read integration row: %v", err)
		}
		if access != nil {
			t.Errorf("tokens should not be stored on invalid state, got access=%q", *access)
		}
	})

	t.Run("missing code → error redirect", func(t *testing.T) {
		org, owner := newOrgWithOwner(t, ts)
		if _, err := svc.SaveCredentials(ctx, mustUUID(t, owner), mustUUID(t, org), SaveFreeAgentCredentialsRequest{ClientID: "cid", ClientSecret: "sec"}); err != nil {
			t.Fatalf("save creds: %v", err)
		}
		state, _ := ts.tokenMaker.CreateToken(mustUUID(t, owner), mustUUID(t, org), time.Minute)

		redirectURL, _ := svc.HandleCallback(ctx, "", state)
		if !strings.Contains(redirectURL, "reason=missing_code") {
			t.Errorf("expected missing_code error redirect, got %q", redirectURL)
		}
	})
}

// =============================================================================
// DISCONNECT
// =============================================================================

func TestFreeAgentIntegration_Disconnect(t *testing.T) {
	ts := newTestServer(t)
	t.Cleanup(func() { ts.pool.Close() })

	ctx := context.Background()
	fake := fakeFreeAgentTokenServer(t)
	svc := freeAgentServiceWithHost(ts, fake.URL)

	org, owner := newOrgWithOwner(t, ts)
	if _, err := svc.SaveCredentials(ctx, mustUUID(t, owner), mustUUID(t, org), SaveFreeAgentCredentialsRequest{ClientID: "cid", ClientSecret: "sec"}); err != nil {
		t.Fatalf("save creds: %v", err)
	}
	state, _ := ts.tokenMaker.CreateToken(mustUUID(t, owner), mustUUID(t, org), time.Minute)
	if _, err := svc.HandleCallback(ctx, "code", state); err != nil {
		t.Fatalf("connect: %v", err)
	}

	// Disconnect clears the tokens but keeps the credentials.
	if err := svc.Disconnect(ctx, mustUUID(t, owner), mustUUID(t, org)); err != nil {
		t.Fatalf("disconnect: %v", err)
	}

	status, err := svc.GetStatus(ctx, mustUUID(t, owner), mustUUID(t, org))
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if status.Connected {
		t.Error("expected connected=false after disconnect")
	}
	if !status.HasCredentials {
		t.Error("expected has_credentials=true (disconnect keeps credentials)")
	}

	// access_token cleared in the DB.
	var access *string
	if err := ts.pool.QueryRow(ctx,
		`SELECT access_token FROM organisation_integrations WHERE organisation_id=$1 AND provider='freeagent'`,
		org).Scan(&access); err != nil {
		t.Fatalf("read integration row: %v", err)
	}
	if access != nil {
		t.Errorf("access_token should be NULL after disconnect, got %q", *access)
	}
}

// =============================================================================
// EXPENSE PUSH STATUS (the detail-page badge)
// =============================================================================

func TestFreeAgentIntegration_PushStatus(t *testing.T) {
	ts := newTestServer(t)
	defer ts.pool.Close()
	ctx := context.Background()
	svc := ts.server.integrationService

	// connect seeds a credentialed + connected integration WITHOUT the OAuth dance
	// (the connect flow is covered elsewhere): save creds via the service, then flip
	// connected_at + a token directly. The row cascade-deletes with the org.
	connect := func(t *testing.T, org, owner string) {
		t.Helper()
		if _, err := svc.SaveCredentials(ctx, mustUUID(t, owner), mustUUID(t, org),
			SaveFreeAgentCredentialsRequest{ClientID: "cid", ClientSecret: "sec"}); err != nil {
			t.Fatalf("save creds: %v", err)
		}
		if _, err := ts.pool.Exec(ctx,
			`UPDATE organisation_integrations SET access_token = 'tok', connected_at = now()
			 WHERE organisation_id = $1 AND provider = 'freeagent'`, org); err != nil {
			t.Fatalf("mark connected: %v", err)
		}
	}

	t.Run("successful push → state pushed, external_url, connected", func(t *testing.T) {
		org, owner := newOrgWithOwner(t, ts)
		connect(t, org, owner)
		expenseID := createExpenseAs(t, ts, owner, org)
		const faURL = "https://api.sandbox.freeagent.com/v2/expenses/77"
		if err := svc.RecordPushResult(ctx, mustUUID(t, org), mustUUID(t, expenseID), faURL, ""); err != nil {
			t.Fatalf("record push: %v", err)
		}

		rec := getFreeAgentPushStatus(t, ts, bearer(t, ts, owner, org), expenseID)
		if rec.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d — body: %s", rec.Code, rec.Body.String())
		}
		got := decodeFAPush(t, rec.Body.Bytes())
		if got.State != "pushed" {
			t.Errorf("state: got %q, want pushed", got.State)
		}
		if got.ExternalURL == nil || *got.ExternalURL != faURL {
			t.Errorf("external_url: got %v, want %q", got.ExternalURL, faURL)
		}
		if !got.Connected {
			t.Error("connected: expected true")
		}
		if got.PushedAt == nil {
			t.Error("pushed_at: expected a timestamp")
		}
	})

	t.Run("failed push → state failed, error message", func(t *testing.T) {
		org, owner := newOrgWithOwner(t, ts)
		connect(t, org, owner)
		expenseID := createExpenseAs(t, ts, owner, org)
		const pushErr = "freeagent rejected: bad category"
		if err := svc.RecordPushResult(ctx, mustUUID(t, org), mustUUID(t, expenseID), "", pushErr); err != nil {
			t.Fatalf("record push: %v", err)
		}

		got := decodeFAPush(t, getFreeAgentPushStatus(t, ts, bearer(t, ts, owner, org), expenseID).Body.Bytes())
		if got.State != "failed" {
			t.Errorf("state: got %q, want failed", got.State)
		}
		if got.Error == nil || *got.Error != pushErr {
			t.Errorf("error: got %v, want %q", got.Error, pushErr)
		}
		if got.ExternalURL != nil {
			t.Errorf("external_url: expected nil on failure, got %q", *got.ExternalURL)
		}
	})

	t.Run("connected but never pushed → state none", func(t *testing.T) {
		org, owner := newOrgWithOwner(t, ts)
		connect(t, org, owner)
		expenseID := createExpenseAs(t, ts, owner, org)

		got := decodeFAPush(t, getFreeAgentPushStatus(t, ts, bearer(t, ts, owner, org), expenseID).Body.Bytes())
		if got.State != "none" {
			t.Errorf("state: got %q, want none", got.State)
		}
		if !got.Connected {
			t.Error("connected: expected true (integration connected, nothing pushed yet)")
		}
	})

	t.Run("non-admin member → 403", func(t *testing.T) {
		org, owner := newOrgWithOwner(t, ts)
		connect(t, org, owner)
		expenseID := createExpenseAs(t, ts, owner, org)
		member := newMemberUser(t, ts, org)
		if rec := getFreeAgentPushStatus(t, ts, bearer(t, ts, member, org), expenseID); rec.Code != http.StatusForbidden {
			t.Errorf("member push status: expected 403, got %d — body: %s", rec.Code, rec.Body.String())
		}
	})

	t.Run("cross-tenant expense id → state none (no leak)", func(t *testing.T) {
		// Org A: connected, with a pushed expense.
		orgA, ownerA := newOrgWithOwner(t, ts)
		connect(t, orgA, ownerA)
		expenseA := createExpenseAs(t, ts, ownerA, orgA)
		if err := svc.RecordPushResult(ctx, mustUUID(t, orgA), mustUUID(t, expenseA),
			"https://api.sandbox.freeagent.com/v2/expenses/1", ""); err != nil {
			t.Fatalf("record push: %v", err)
		}
		// Org B asks about org A's expense id → org-scoped lookup finds no row.
		orgB, ownerB := newOrgWithOwner(t, ts)
		connect(t, orgB, ownerB)
		got := decodeFAPush(t, getFreeAgentPushStatus(t, ts, bearer(t, ts, ownerB, orgB), expenseA).Body.Bytes())
		if got.State != "none" {
			t.Errorf("cross-tenant: got state %q, want none (must not leak org A's push)", got.State)
		}
	})
}

// =============================================================================
// MULTI-TENANT ISOLATION
// =============================================================================

func TestFreeAgentIntegration_TenantIsolation(t *testing.T) {
	ts := newTestServer(t)
	defer ts.pool.Close()

	org, _ := newOrgWithOwner(t, ts)
	// devUserID is a member of the dev org, NOT org. A token scoped to org is
	// rejected by the membership check on every integration endpoint.
	outsider := bearer(t, ts, devUserID, org)

	if rec := getFreeAgentStatus(t, ts, outsider); rec.Code != http.StatusForbidden {
		t.Errorf("cross-tenant GET status: expected 403, got %d", rec.Code)
	}
	if rec := putFreeAgentCreds(t, ts, outsider, SaveFreeAgentCredentialsRequest{ClientID: "x", ClientSecret: "y"}); rec.Code != http.StatusForbidden {
		t.Errorf("cross-tenant PUT creds: expected 403, got %d", rec.Code)
	}
	if rec := deleteFreeAgentReq(t, ts, outsider); rec.Code != http.StatusForbidden {
		t.Errorf("cross-tenant DELETE: expected 403, got %d", rec.Code)
	}
}
