package main

// platform_admin_test.go
// =============================================================================
// Integration tests for the platform-admin ("god view") endpoints
// (GET /api/v1/admin/*). These hit the REAL database via the shared newTestServer
// harness and skip cleanly when DATABASE_URL is unset.
//
// The god view is the ONE cross-tenant surface, gated purely by
// users.is_superuser. Coverage: the superuser gate (a normal member → 403,
// unauthenticated → 401 on every route); the list reads span multiple tenants;
// the drill-ins return members / memberships; and a malformed :id → 400. The
// shared dev DB already holds many orgs/users, so assertions check that the rows
// THIS test created are present, never exact counts.
// =============================================================================

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"

	organisation "github.com/operationfb/accounting-saas/internal/organisation"
	platformadmin "github.com/operationfb/accounting-saas/internal/platformadmin"
)

// newSuperuser inserts an active user with is_superuser = TRUE (no org membership
// needed — the god view is not org-scoped) and registers cleanup. Returns its id.
func newSuperuser(t *testing.T, ts *testServer) string {
	t.Helper()
	id := uuid.NewString()
	email := "super-" + id + "@test.local"
	if _, err := ts.pool.Exec(context.Background(),
		`INSERT INTO users (id, email, first_name, last_name, is_active, is_superuser, email_verified_at)
		 VALUES ($1, $2, 'Super', 'User', TRUE, TRUE, now())`, id, email); err != nil {
		t.Fatalf("newSuperuser: %v", err)
	}
	t.Cleanup(func() { _, _ = ts.pool.Exec(context.Background(), `DELETE FROM users WHERE id = $1`, id) })
	return id
}

// newBareUser inserts an active user with NO org membership (for the "add existing
// user to an org" path) and registers cleanup (its memberships + the user). Returns id.
func newBareUser(t *testing.T, ts *testServer) string {
	t.Helper()
	id := uuid.NewString()
	email := "bare-" + id + "@test.local"
	if _, err := ts.pool.Exec(context.Background(),
		`INSERT INTO users (id, email, first_name, last_name, is_active, email_verified_at)
		 VALUES ($1, $2, 'Bare', 'User', TRUE, now())`, id, email); err != nil {
		t.Fatalf("newBareUser: %v", err)
	}
	t.Cleanup(func() {
		_, _ = ts.pool.Exec(context.Background(), `DELETE FROM organisation_memberships WHERE user_id = $1`, id)
		_, _ = ts.pool.Exec(context.Background(), `DELETE FROM users WHERE id = $1`, id)
	})
	return id
}

// getAdmin sends GET to an /api/v1/admin path with the given auth header.
func getAdmin(t *testing.T, ts *testServer, path, authHeader string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodGet, path, nil)
	if authHeader != "" {
		req.Header.Set("Authorization", authHeader)
	}
	ts.server.router.ServeHTTP(rec, req)
	return rec
}

// TestPlatformAdminAuthorization asserts every /admin route is superuser-gated.
func TestPlatformAdminAuthorization(t *testing.T) {
	ts := newTestServer(t)
	defer ts.pool.Close()

	// A normal org owner (not a superuser) and a valid org id for their token.
	orgA, owner := newOrgWithOwner(t, ts)
	normalAuth := bearer(t, ts, owner, orgA)

	paths := []string{
		"/api/v1/admin/organisations",
		"/api/v1/admin/organisations/" + orgA,
		"/api/v1/admin/users",
		"/api/v1/admin/users/" + owner,
	}

	t.Run("normal member → 403 on every route", func(t *testing.T) {
		for _, p := range paths {
			rec := getAdmin(t, ts, p, normalAuth)
			if rec.Code != http.StatusForbidden {
				t.Errorf("%s: expected 403, got %d — body: %s", p, rec.Code, rec.Body.String())
			}
		}
	})

	t.Run("unauthenticated → 401 on every route", func(t *testing.T) {
		for _, p := range paths {
			rec := getAdmin(t, ts, p, "")
			if rec.Code != http.StatusUnauthorized {
				t.Errorf("%s: expected 401, got %d — body: %s", p, rec.Code, rec.Body.String())
			}
		}
	})
}

// TestPlatformAdminListOrganisations covers GET /api/v1/admin/organisations —
// the superuser sees orgs across tenants.
func TestPlatformAdminListOrganisations(t *testing.T) {
	ts := newTestServer(t)
	defer ts.pool.Close()

	super := newSuperuser(t, ts)
	orgA, _ := newOrgWithOwner(t, ts) // has one member (the owner)
	orgB := newBareOrg(t, ts)         // has zero members

	// The token org is irrelevant for the god view; any valid one works.
	rec := getAdmin(t, ts, "/api/v1/admin/organisations", bearer(t, ts, super, devOrgID))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d — body: %s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Organisations []platformadmin.AdminOrganisationResponse `json:"organisations"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v — body: %s", err, rec.Body.String())
	}
	byID := map[string]platformadmin.AdminOrganisationResponse{}
	for _, o := range resp.Organisations {
		byID[o.ID] = o
	}
	// Both my throwaway orgs (from different "tenants") must appear — proves the
	// read is cross-tenant.
	if _, ok := byID[orgA]; !ok {
		t.Errorf("orgA %s missing from cross-tenant list", orgA)
	}
	if got, ok := byID[orgB]; !ok {
		t.Errorf("orgB %s missing from cross-tenant list", orgB)
	} else if got.MemberCount != 0 {
		t.Errorf("orgB member_count: got %d, want 0", got.MemberCount)
	}
	if got := byID[orgA]; got.MemberCount != 1 {
		t.Errorf("orgA member_count: got %d, want 1", got.MemberCount)
	}
}

// TestPlatformAdminListUsers covers GET /api/v1/admin/users, including the
// is_superuser badge.
func TestPlatformAdminListUsers(t *testing.T) {
	ts := newTestServer(t)
	defer ts.pool.Close()

	super := newSuperuser(t, ts)
	_, owner := newOrgWithOwner(t, ts)

	rec := getAdmin(t, ts, "/api/v1/admin/users", bearer(t, ts, super, devOrgID))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d — body: %s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Users []platformadmin.AdminUserResponse `json:"users"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v — body: %s", err, rec.Body.String())
	}
	byID := map[string]platformadmin.AdminUserResponse{}
	for _, u := range resp.Users {
		byID[u.ID] = u
	}
	if u, ok := byID[super]; !ok {
		t.Errorf("superuser %s missing from user list", super)
	} else if !u.IsSuperuser {
		t.Errorf("superuser row is_superuser: got false, want true")
	}
	if u, ok := byID[owner]; !ok {
		t.Errorf("owner %s missing from user list", owner)
	} else if u.IsSuperuser {
		t.Errorf("normal owner is_superuser: got true, want false")
	}
}

// TestPlatformAdminGetOrganisation covers the org drill-in (members list).
func TestPlatformAdminGetOrganisation(t *testing.T) {
	ts := newTestServer(t)
	defer ts.pool.Close()

	super := newSuperuser(t, ts)
	orgA, owner := newOrgWithOwner(t, ts)

	rec := getAdmin(t, ts, "/api/v1/admin/organisations/"+orgA, bearer(t, ts, super, devOrgID))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d — body: %s", rec.Code, rec.Body.String())
	}
	var resp platformadmin.AdminOrganisationDetailResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v — body: %s", err, rec.Body.String())
	}
	if resp.Organisation.ID != orgA {
		t.Errorf("org id: got %s, want %s", resp.Organisation.ID, orgA)
	}
	found := false
	for _, m := range resp.Members {
		if m.UserID == owner {
			found = true
			if m.Role != "owner" {
				t.Errorf("owner role: got %q, want owner", m.Role)
			}
		}
	}
	if !found {
		t.Errorf("owner %s not in org members", owner)
	}

	t.Run("unknown org → 404", func(t *testing.T) {
		rec := getAdmin(t, ts, "/api/v1/admin/organisations/"+uuid.NewString(), bearer(t, ts, super, devOrgID))
		if rec.Code != http.StatusNotFound {
			t.Errorf("expected 404, got %d — body: %s", rec.Code, rec.Body.String())
		}
	})

	t.Run("malformed org id → 400", func(t *testing.T) {
		rec := getAdmin(t, ts, "/api/v1/admin/organisations/not-a-uuid", bearer(t, ts, super, devOrgID))
		if rec.Code != http.StatusBadRequest {
			t.Errorf("expected 400, got %d — body: %s", rec.Code, rec.Body.String())
		}
	})
}

// TestPlatformAdminGetUser covers the user drill-in: every org the user belongs
// to, across tenants.
func TestPlatformAdminGetUser(t *testing.T) {
	ts := newTestServer(t)
	defer ts.pool.Close()

	super := newSuperuser(t, ts)
	orgA, user := newOrgWithOwner(t, ts) // owner of orgA
	orgB := newBareOrg(t, ts)
	addMembershipTo(t, ts, orgB, user, "member", "active")

	rec := getAdmin(t, ts, "/api/v1/admin/users/"+user, bearer(t, ts, super, devOrgID))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d — body: %s", rec.Code, rec.Body.String())
	}
	var resp platformadmin.AdminUserDetailResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v — body: %s", err, rec.Body.String())
	}
	if resp.User.ID != user {
		t.Errorf("user id: got %s, want %s", resp.User.ID, user)
	}
	roleByOrg := map[string]string{}
	for _, m := range resp.Memberships {
		roleByOrg[m.OrganisationID] = m.Role
	}
	if roleByOrg[orgA] != "owner" {
		t.Errorf("orgA role: got %q, want owner", roleByOrg[orgA])
	}
	if roleByOrg[orgB] != "member" {
		t.Errorf("orgB role: got %q, want member", roleByOrg[orgB])
	}
}

// putAdminJSON sends a PUT with a JSON body to an /api/v1/admin path.
func putAdminJSON(t *testing.T, ts *testServer, path, authHeader string, body any) *httptest.ResponseRecorder {
	t.Helper()
	b, _ := json.Marshal(body)
	rec := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodPut, path, bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	if authHeader != "" {
		req.Header.Set("Authorization", authHeader)
	}
	ts.server.router.ServeHTTP(rec, req)
	return rec
}

// decodeAdminOrgDetails pulls the { "organisation": {...} } envelope the admin
// company-details endpoints return.
func decodeAdminOrgDetails(t *testing.T, body []byte) organisation.OrganisationDetailsResponse {
	t.Helper()
	var resp struct {
		Organisation organisation.OrganisationDetailsResponse `json:"organisation"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		t.Fatalf("decode org details: %v — body: %s", err, string(body))
	}
	return resp.Organisation
}

// TestPlatformAdminCompanyDetails covers the god-view's editable surface:
// GET/PUT /api/v1/admin/organisations/:id/company-details.
func TestPlatformAdminCompanyDetails(t *testing.T) {
	ts := newTestServer(t)
	defer ts.pool.Close()

	super := newSuperuser(t, ts)
	orgA, owner := newOrgWithOwner(t, ts) // a throwaway target org (GB / GBP)
	path := "/api/v1/admin/organisations/" + orgA + "/company-details"

	t.Run("superuser GET returns the target org's details (cross-tenant)", func(t *testing.T) {
		rec := getAdmin(t, ts, path, bearer(t, ts, super, devOrgID))
		if rec.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d — body: %s", rec.Code, rec.Body.String())
		}
		got := decodeAdminOrgDetails(t, rec.Body.Bytes())
		if got.ID != orgA {
			t.Errorf("id: got %q, want %q", got.ID, orgA)
		}
		if got.CountryCode != "GB" || got.NativeCurrency != "GBP" {
			t.Errorf("country/currency: got %q/%q, want GB/GBP", got.CountryCode, got.NativeCurrency)
		}
	})

	t.Run("superuser PUT edits the name, persists, and preserves country+currency", func(t *testing.T) {
		// Seed non-default immutable fields to prove the admin PUT leaves them.
		if _, err := ts.pool.Exec(context.Background(),
			`UPDATE organisations SET native_currency = 'USD', country_code = 'FR' WHERE id = $1`, orgA); err != nil {
			t.Fatalf("seed immutable fields: %v", err)
		}

		rec := putAdminJSON(t, ts, path, bearer(t, ts, super, devOrgID),
			organisation.UpdateOrganisationRequest{Name: "Renamed By Admin", CompanyType: ptr("limited"), CompaniesHouseNumber: ptr("12345678")})
		if rec.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d — body: %s", rec.Code, rec.Body.String())
		}
		got := decodeAdminOrgDetails(t, rec.Body.Bytes())
		if got.Name != "Renamed By Admin" {
			t.Errorf("name: got %q, want %q", got.Name, "Renamed By Admin")
		}
		// Immutable fields untouched by the admin edit.
		if got.CountryCode != "FR" || got.NativeCurrency != "USD" {
			t.Errorf("country/currency changed: got %q/%q, want FR/USD (immutable)", got.CountryCode, got.NativeCurrency)
		}

		// Persisted in the DB.
		var dbName, dbCountry, dbCur string
		if err := ts.pool.QueryRow(context.Background(),
			`SELECT name, country_code, native_currency FROM organisations WHERE id = $1`, orgA).
			Scan(&dbName, &dbCountry, &dbCur); err != nil {
			t.Fatalf("read org: %v", err)
		}
		if dbName != "Renamed By Admin" || dbCountry != "FR" || dbCur != "USD" {
			t.Errorf("DB mismatch: name=%q country=%q currency=%q", dbName, dbCountry, dbCur)
		}
	})

	t.Run("non-superuser → 403 on GET and PUT", func(t *testing.T) {
		// The org's own owner is NOT a superuser — the god-view surface refuses them.
		ownerAuth := bearer(t, ts, owner, orgA)
		if rec := getAdmin(t, ts, path, ownerAuth); rec.Code != http.StatusForbidden {
			t.Errorf("GET: expected 403, got %d — body: %s", rec.Code, rec.Body.String())
		}
		rec := putAdminJSON(t, ts, path, ownerAuth, organisation.UpdateOrganisationRequest{Name: "Nope"})
		if rec.Code != http.StatusForbidden {
			t.Errorf("PUT: expected 403, got %d — body: %s", rec.Code, rec.Body.String())
		}
	})

	t.Run("unknown org → 404", func(t *testing.T) {
		rec := getAdmin(t, ts, "/api/v1/admin/organisations/"+uuid.NewString()+"/company-details",
			bearer(t, ts, super, devOrgID))
		if rec.Code != http.StatusNotFound {
			t.Errorf("expected 404, got %d — body: %s", rec.Code, rec.Body.String())
		}
	})

	t.Run("malformed id → 400", func(t *testing.T) {
		rec := getAdmin(t, ts, "/api/v1/admin/organisations/not-a-uuid/company-details",
			bearer(t, ts, super, devOrgID))
		if rec.Code != http.StatusBadRequest {
			t.Errorf("expected 400, got %d — body: %s", rec.Code, rec.Body.String())
		}
	})
}

// postAdminJSON sends a POST with a JSON body to an /api/v1/admin path.
func postAdminJSON(t *testing.T, ts *testServer, path, authHeader string, body any) *httptest.ResponseRecorder {
	t.Helper()
	b, _ := json.Marshal(body)
	rec := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodPost, path, bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	if authHeader != "" {
		req.Header.Set("Authorization", authHeader)
	}
	ts.server.router.ServeHTTP(rec, req)
	return rec
}

// decodeAdminOrg pulls the { "organisation": {...} } summary envelope the create
// endpoint returns.
func decodeAdminOrg(t *testing.T, body []byte) platformadmin.AdminOrganisationResponse {
	t.Helper()
	var resp struct {
		Organisation platformadmin.AdminOrganisationResponse `json:"organisation"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		t.Fatalf("decode org: %v — body: %s", err, string(body))
	}
	return resp.Organisation
}

// TestPlatformAdminCreateOrganisation covers POST /api/v1/admin/organisations —
// creating an org AND provisioning its chart of accounts in one transaction.
func TestPlatformAdminCreateOrganisation(t *testing.T) {
	ts := newTestServer(t)
	defer ts.pool.Close()

	super := newSuperuser(t, ts)
	path := "/api/v1/admin/organisations"

	t.Run("superuser creates an org + provisions its CoA", func(t *testing.T) {
		name := "Create Test Co " + uuid.NewString()
		rec := postAdminJSON(t, ts, path, bearer(t, ts, super, devOrgID),
			platformadmin.CreateOrganisationRequest{Name: name, CountryCode: "GB", NativeCurrency: "GBP"})
		if rec.Code != http.StatusCreated {
			t.Fatalf("expected 201, got %d — body: %s", rec.Code, rec.Body.String())
		}
		got := decodeAdminOrg(t, rec.Body.Bytes())
		if got.Name != name || got.CountryCode != "GB" || got.MemberCount != 0 {
			t.Errorf("unexpected org: %+v", got)
		}
		// Clean up the org + its provisioned categories (fresh org has no GL rows).
		t.Cleanup(func() {
			_, _ = ts.pool.Exec(context.Background(), `DELETE FROM categories WHERE organisation_id = $1`, got.ID)
			_, _ = ts.pool.Exec(context.Background(), `DELETE FROM organisations WHERE id = $1`, got.ID)
		})

		// The CoA must match exactly what ProvisionChart copies for GB — the same
		// country-or-global template selection the production query uses.
		var want, have int
		if err := ts.pool.QueryRow(context.Background(), `
			SELECT count(*) FROM chart_template t
			WHERE t.country_code IS NOT DISTINCT FROM (
			  SELECT CASE WHEN EXISTS (SELECT 1 FROM chart_template ct WHERE ct.country_code = 'GB')
			              THEN 'GB'::char(2) ELSE NULL::char(2) END)`).Scan(&want); err != nil {
			t.Fatalf("count template: %v", err)
		}
		if err := ts.pool.QueryRow(context.Background(),
			`SELECT count(*) FROM categories WHERE organisation_id = $1`, got.ID).Scan(&have); err != nil {
			t.Fatalf("count categories: %v", err)
		}
		if want == 0 || have != want {
			t.Errorf("provisioned categories: got %d, want %d", have, want)
		}
		// Spot-check a well-known account came through.
		var sales int
		if err := ts.pool.QueryRow(context.Background(),
			`SELECT count(*) FROM categories WHERE organisation_id = $1 AND nominal_code = '001'`, got.ID).Scan(&sales); err != nil {
			t.Fatalf("check 001: %v", err)
		}
		if sales != 1 {
			t.Errorf("nominal '001' (Sales) not provisioned")
		}
	})

	t.Run("non-superuser → 403", func(t *testing.T) {
		_, owner := newOrgWithOwner(t, ts) // a normal owner, not a superuser
		rec := postAdminJSON(t, ts, path, bearer(t, ts, owner, devOrgID),
			platformadmin.CreateOrganisationRequest{Name: "Nope", CountryCode: "GB", NativeCurrency: "GBP"})
		if rec.Code != http.StatusForbidden {
			t.Errorf("expected 403, got %d — body: %s", rec.Code, rec.Body.String())
		}
	})

	t.Run("missing name → 400", func(t *testing.T) {
		rec := postAdminJSON(t, ts, path, bearer(t, ts, super, devOrgID),
			platformadmin.CreateOrganisationRequest{CountryCode: "GB", NativeCurrency: "GBP"})
		if rec.Code != http.StatusBadRequest {
			t.Errorf("expected 400, got %d — body: %s", rec.Code, rec.Body.String())
		}
	})

	t.Run("unknown currency → 422", func(t *testing.T) {
		rec := postAdminJSON(t, ts, path, bearer(t, ts, super, devOrgID),
			platformadmin.CreateOrganisationRequest{Name: "Bad Cur " + uuid.NewString(), CountryCode: "GB", NativeCurrency: "XYZ"})
		if rec.Code != http.StatusUnprocessableEntity {
			t.Errorf("expected 422, got %d — body: %s", rec.Code, rec.Body.String())
		}
	})

	t.Run("duplicate name (slug clash) → 409", func(t *testing.T) {
		name := "Dup Co " + uuid.NewString()
		body := platformadmin.CreateOrganisationRequest{Name: name, CountryCode: "GB", NativeCurrency: "GBP"}
		rec := postAdminJSON(t, ts, path, bearer(t, ts, super, devOrgID), body)
		if rec.Code != http.StatusCreated {
			t.Fatalf("first create: expected 201, got %d — body: %s", rec.Code, rec.Body.String())
		}
		firstID := decodeAdminOrg(t, rec.Body.Bytes()).ID
		t.Cleanup(func() {
			_, _ = ts.pool.Exec(context.Background(), `DELETE FROM categories WHERE organisation_id = $1`, firstID)
			_, _ = ts.pool.Exec(context.Background(), `DELETE FROM organisations WHERE id = $1`, firstID)
		})
		// Same name → same derived slug → UNIQUE violation → 409.
		rec2 := postAdminJSON(t, ts, path, bearer(t, ts, super, devOrgID), body)
		if rec2.Code != http.StatusConflict {
			t.Errorf("duplicate: expected 409, got %d — body: %s", rec2.Code, rec2.Body.String())
		}
	})
}

// orgDetailMemberRoles decodes the (unwrapped) org-detail the member-mutation
// endpoints return and maps user_id → role.
func orgDetailMemberRoles(t *testing.T, body []byte) map[string]string {
	t.Helper()
	var resp platformadmin.AdminOrganisationDetailResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		t.Fatalf("decode org detail: %v — body: %s", err, string(body))
	}
	out := map[string]string{}
	for _, m := range resp.Members {
		out[m.UserID] = m.Role
	}
	return out
}

// TestPlatformAdminAddMember covers POST /admin/organisations/:id/members — adding
// an EXISTING user to an org.
func TestPlatformAdminAddMember(t *testing.T) {
	ts := newTestServer(t)
	defer ts.pool.Close()

	super := newSuperuser(t, ts)
	orgA, _ := newOrgWithOwner(t, ts)
	user := newBareUser(t, ts)
	path := "/api/v1/admin/organisations/" + orgA + "/members"

	t.Run("superuser adds an existing user → 201, appears as member", func(t *testing.T) {
		rec := postAdminJSON(t, ts, path, bearer(t, ts, super, devOrgID),
			platformadmin.AddOrganisationMemberRequest{UserID: user, Role: "admin"})
		if rec.Code != http.StatusCreated {
			t.Fatalf("expected 201, got %d — body: %s", rec.Code, rec.Body.String())
		}
		if roles := orgDetailMemberRoles(t, rec.Body.Bytes()); roles[user] != "admin" {
			t.Errorf("member role: got %q, want admin (members=%v)", roles[user], roles)
		}
		// Membership row committed.
		var n int
		if err := ts.pool.QueryRow(context.Background(),
			`SELECT count(*) FROM organisation_memberships WHERE organisation_id=$1 AND user_id=$2 AND status='active'`,
			orgA, user).Scan(&n); err != nil {
			t.Fatalf("count membership: %v", err)
		}
		if n != 1 {
			t.Errorf("membership not created: count=%d", n)
		}
	})

	t.Run("adding the same user again → 409", func(t *testing.T) {
		rec := postAdminJSON(t, ts, path, bearer(t, ts, super, devOrgID),
			platformadmin.AddOrganisationMemberRequest{UserID: user, Role: "member"})
		if rec.Code != http.StatusConflict {
			t.Errorf("expected 409, got %d — body: %s", rec.Code, rec.Body.String())
		}
	})

	t.Run("non-superuser → 403", func(t *testing.T) {
		_, owner := newOrgWithOwner(t, ts)
		rec := postAdminJSON(t, ts, path, bearer(t, ts, owner, devOrgID),
			platformadmin.AddOrganisationMemberRequest{UserID: newBareUser(t, ts), Role: "member"})
		if rec.Code != http.StatusForbidden {
			t.Errorf("expected 403, got %d — body: %s", rec.Code, rec.Body.String())
		}
	})

	t.Run("unknown user → 404", func(t *testing.T) {
		rec := postAdminJSON(t, ts, path, bearer(t, ts, super, devOrgID),
			platformadmin.AddOrganisationMemberRequest{UserID: uuid.NewString(), Role: "member"})
		if rec.Code != http.StatusNotFound {
			t.Errorf("expected 404, got %d — body: %s", rec.Code, rec.Body.String())
		}
	})

	t.Run("bad role → 400", func(t *testing.T) {
		rec := postAdminJSON(t, ts, path, bearer(t, ts, super, devOrgID),
			platformadmin.AddOrganisationMemberRequest{UserID: newBareUser(t, ts), Role: "superuser"})
		if rec.Code != http.StatusBadRequest {
			t.Errorf("expected 400, got %d — body: %s", rec.Code, rec.Body.String())
		}
	})
}

// TestPlatformAdminCreateUser covers POST /admin/organisations/:id/users — creating
// a NEW user under an org.
func TestPlatformAdminCreateUser(t *testing.T) {
	ts := newTestServer(t)
	defer ts.pool.Close()

	super := newSuperuser(t, ts)
	orgA, _ := newOrgWithOwner(t, ts)
	path := "/api/v1/admin/organisations/" + orgA + "/users"

	t.Run("superuser creates a new user as owner → 201, user + membership exist", func(t *testing.T) {
		email := "created-" + uuid.NewString() + "@test.local"
		rec := postAdminJSON(t, ts, path, bearer(t, ts, super, devOrgID),
			platformadmin.CreateOrganisationUserRequest{
				Email: email, Password: "password123", FirstName: "New", LastName: "Owner", Role: "owner",
			})
		if rec.Code != http.StatusCreated {
			t.Fatalf("expected 201, got %d — body: %s", rec.Code, rec.Body.String())
		}
		// Find the new user's id in the returned member list and clean it up.
		var newID string
		if err := ts.pool.QueryRow(context.Background(),
			`SELECT id::text FROM users WHERE email=$1`, email).Scan(&newID); err != nil {
			t.Fatalf("read new user: %v", err)
		}
		t.Cleanup(func() {
			_, _ = ts.pool.Exec(context.Background(), `DELETE FROM organisation_memberships WHERE user_id=$1`, newID)
			_, _ = ts.pool.Exec(context.Background(), `DELETE FROM users WHERE id=$1`, newID)
		})
		if roles := orgDetailMemberRoles(t, rec.Body.Bytes()); roles[newID] != "owner" {
			t.Errorf("new member role: got %q, want owner", roles[newID])
		}
		// Active membership committed.
		var n int
		if err := ts.pool.QueryRow(context.Background(),
			`SELECT count(*) FROM organisation_memberships WHERE organisation_id=$1 AND user_id=$2 AND status='active'`,
			orgA, newID).Scan(&n); err != nil {
			t.Fatalf("count membership: %v", err)
		}
		if n != 1 {
			t.Errorf("membership not created: count=%d", n)
		}

		// Duplicate email → 409.
		rec2 := postAdminJSON(t, ts, path, bearer(t, ts, super, devOrgID),
			platformadmin.CreateOrganisationUserRequest{
				Email: email, Password: "password123", FirstName: "Dup", LastName: "User", Role: "member",
			})
		if rec2.Code != http.StatusConflict {
			t.Errorf("duplicate email: expected 409, got %d — body: %s", rec2.Code, rec2.Body.String())
		}
	})

	t.Run("non-superuser → 403", func(t *testing.T) {
		_, owner := newOrgWithOwner(t, ts)
		rec := postAdminJSON(t, ts, path, bearer(t, ts, owner, devOrgID),
			platformadmin.CreateOrganisationUserRequest{
				Email: "x-" + uuid.NewString() + "@test.local", Password: "password123",
				FirstName: "A", LastName: "B", Role: "member",
			})
		if rec.Code != http.StatusForbidden {
			t.Errorf("expected 403, got %d — body: %s", rec.Code, rec.Body.String())
		}
	})

	t.Run("missing fields → 400", func(t *testing.T) {
		rec := postAdminJSON(t, ts, path, bearer(t, ts, super, devOrgID),
			platformadmin.CreateOrganisationUserRequest{Email: "not-an-email", Role: "member"})
		if rec.Code != http.StatusBadRequest {
			t.Errorf("expected 400, got %d — body: %s", rec.Code, rec.Body.String())
		}
	})
}
