package main

// user_service_test.go
// =============================================================================
// Integration tests for the user "My Details" endpoints
// (GET/PUT /api/v1/profile).
//
// Like the rest of the suite these hit a REAL PostgreSQL database via the shared
// newTestServer harness (server_test.go) and skip cleanly when DATABASE_URL is
// unset. To avoid mutating the shared dev user (which other tests read), the
// tests run against a throwaway user created by newOrgWithOwner — whose row is
// deleted on cleanup.
//
// Coverage: GET returns the caller's own row (self-scoped); update happy path +
// re-GET + DB persistence; the read-modify-write PRESERVATION of fields this form
// does not own (phone / avatar_url); validation (binding 400 on a missing name +
// service-layer 422 on a whitespace-only name); and 401 when unauthenticated.
// There is no money here, so no decimal-conversion test; and no role/multi-tenant
// matrix because /profile is always self-scoped from the token (you can only ever
// read/edit yourself — there is no id to pass).
// =============================================================================

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"

	userauth "github.com/operationfb/accounting-saas/internal/userauth"
)

// =============================================================================
// PROFILE TEST HELPERS
// =============================================================================

// putProfile sends PUT /api/v1/profile with the given auth header (empty = none)
// and JSON body, returning the recorder.
func putProfile(t *testing.T, ts *testServer, authHeader string, body userauth.UpdateProfileRequest) *httptest.ResponseRecorder {
	t.Helper()
	bodyBytes, _ := json.Marshal(body)
	rec := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodPut, "/api/v1/profile", bytes.NewReader(bodyBytes))
	req.Header.Set("Content-Type", "application/json")
	if authHeader != "" {
		req.Header.Set("Authorization", authHeader)
	}
	ts.server.router.ServeHTTP(rec, req)
	return rec
}

// getProfileReq sends GET /api/v1/profile.
func getProfileReq(t *testing.T, ts *testServer, authHeader string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodGet, "/api/v1/profile", nil)
	if authHeader != "" {
		req.Header.Set("Authorization", authHeader)
	}
	ts.server.router.ServeHTTP(rec, req)
	return rec
}

// decodeProfile pulls the { "user": {...} } envelope into a userauth.UserResponse.
func decodeProfile(t *testing.T, body []byte) userauth.UserResponse {
	t.Helper()
	var resp struct {
		User userauth.UserResponse `json:"user"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		t.Fatalf("decode profile: %v — body: %s", err, string(body))
	}
	return resp.User
}

// =============================================================================
// GET
// =============================================================================

// TestHandleGetProfile covers GET /api/v1/profile — it returns the caller's OWN
// row, identified purely by the token (self-scoped).
func TestHandleGetProfile(t *testing.T) {
	t.Parallel()
	ts := newTestServer(t)
	t.Cleanup(func() { ts.pool.Close() })

	t.Run("returns the caller's own profile", func(t *testing.T) {
		orgB, ownerB := newOrgWithOwner(t, ts)

		rec := getProfileReq(t, ts, bearer(t, ts, ownerB, orgB))
		if rec.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d — body: %s", rec.Code, rec.Body.String())
		}
		got := decodeProfile(t, rec.Body.Bytes())

		if got.ID != ownerB {
			t.Errorf("id: got %q, want %q", got.ID, ownerB)
		}
		// newOrgWithOwner seeds the user as 'Other Owner'.
		if got.FirstName != "Other" || got.LastName != "Owner" {
			t.Errorf("name: got %q %q, want %q %q", got.FirstName, got.LastName, "Other", "Owner")
		}
		// The email is the seeded "owner-<uuid>@test.local" — proves we read this
		// specific caller, not some other row.
		if got.Email != "owner-"+ownerB+"@test.local" {
			t.Errorf("email: got %q, want %q", got.Email, "owner-"+ownerB+"@test.local")
		}
	})

	t.Run("unauthenticated → 401", func(t *testing.T) {
		rec := getProfileReq(t, ts, "")
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("expected 401, got %d — body: %s", rec.Code, rec.Body.String())
		}
	})
}

// =============================================================================
// UPDATE
// =============================================================================

// TestHandleUpdateProfile covers PUT /api/v1/profile and its input validation.
func TestHandleUpdateProfile(t *testing.T) {
	t.Parallel()
	ts := newTestServer(t)
	t.Cleanup(func() { ts.pool.Close() })

	t.Run("updates own name → 200, round-trips, persists, re-GET reflects", func(t *testing.T) {
		orgB, ownerB := newOrgWithOwner(t, ts)
		authHeader := bearer(t, ts, ownerB, orgB)

		rec := putProfile(t, ts, authHeader, userauth.UpdateProfileRequest{
			FirstName: "Aydin",
			LastName:  "Gunal",
		})
		if rec.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d — body: %s", rec.Code, rec.Body.String())
		}
		got := decodeProfile(t, rec.Body.Bytes())
		if got.ID != ownerB {
			t.Errorf("id: got %q, want %q", got.ID, ownerB)
		}
		if got.FirstName != "Aydin" || got.LastName != "Gunal" {
			t.Errorf("name: got %q %q, want %q %q", got.FirstName, got.LastName, "Aydin", "Gunal")
		}

		// Persisted across a fresh read.
		getRec := getProfileReq(t, ts, authHeader)
		if getRec.Code != http.StatusOK {
			t.Fatalf("re-GET: expected 200, got %d — body: %s", getRec.Code, getRec.Body.String())
		}
		reread := decodeProfile(t, getRec.Body.Bytes())
		if reread.FirstName != "Aydin" || reread.LastName != "Gunal" {
			t.Errorf("re-GET name: got %q %q, want %q %q", reread.FirstName, reread.LastName, "Aydin", "Gunal")
		}

		// Row actually committed — only a real DB can prove this.
		var dbFirst, dbLast string
		if err := ts.pool.QueryRow(context.Background(),
			"SELECT first_name, last_name FROM users WHERE id = $1", ownerB).Scan(&dbFirst, &dbLast); err != nil {
			t.Fatalf("user not found in DB: %v", err)
		}
		if dbFirst != "Aydin" || dbLast != "Gunal" {
			t.Errorf("DB row mismatch: first_name=%q last_name=%q", dbFirst, dbLast)
		}
	})

	t.Run("update trims surrounding whitespace", func(t *testing.T) {
		orgB, ownerB := newOrgWithOwner(t, ts)

		rec := putProfile(t, ts, bearer(t, ts, ownerB, orgB), userauth.UpdateProfileRequest{
			FirstName: "  Jo  ",
			LastName:  "  Bloggs ",
		})
		if rec.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d — body: %s", rec.Code, rec.Body.String())
		}
		got := decodeProfile(t, rec.Body.Bytes())
		if got.FirstName != "Jo" || got.LastName != "Bloggs" {
			t.Errorf("trim: got %q %q, want %q %q", got.FirstName, got.LastName, "Jo", "Bloggs")
		}
	})

	// The form does not touch phone / avatar_url, but the sqlc UpdateUser query
	// owns those columns too — so the service must round-trip them or a name save
	// would wipe them. This guards that read-modify-write preservation.
	t.Run("phone + avatar_url preserved across a name update", func(t *testing.T) {
		orgB, ownerB := newOrgWithOwner(t, ts)
		ctx := context.Background()
		const phone = "+447700900123"
		const avatar = "https://cdn.example/avatars/owner.png"
		if _, err := ts.pool.Exec(ctx,
			"UPDATE users SET phone = $2, avatar_url = $3 WHERE id = $1", ownerB, phone, avatar); err != nil {
			t.Fatalf("seed phone/avatar: %v", err)
		}

		rec := putProfile(t, ts, bearer(t, ts, ownerB, orgB), userauth.UpdateProfileRequest{
			FirstName: "Renamed",
			LastName:  "Person",
		})
		if rec.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d — body: %s", rec.Code, rec.Body.String())
		}
		got := decodeProfile(t, rec.Body.Bytes())
		if got.Phone == nil || *got.Phone != phone {
			t.Errorf("phone not preserved in response: got %v, want %q", got.Phone, phone)
		}
		if got.AvatarURL == nil || *got.AvatarURL != avatar {
			t.Errorf("avatar_url not preserved in response: got %v, want %q", got.AvatarURL, avatar)
		}

		// And in the DB.
		var dbPhone, dbAvatar string
		if err := ts.pool.QueryRow(ctx,
			"SELECT phone, avatar_url FROM users WHERE id = $1", ownerB).Scan(&dbPhone, &dbAvatar); err != nil {
			t.Fatalf("re-read user: %v", err)
		}
		if dbPhone != phone || dbAvatar != avatar {
			t.Errorf("DB not preserved: phone=%q avatar_url=%q", dbPhone, dbAvatar)
		}
	})

	t.Run("missing first_name → 400 binding", func(t *testing.T) {
		orgB, ownerB := newOrgWithOwner(t, ts)
		rec := putProfile(t, ts, bearer(t, ts, ownerB, orgB), userauth.UpdateProfileRequest{LastName: "OnlyLast"})
		if rec.Code != http.StatusBadRequest {
			t.Errorf("expected 400, got %d — body: %s", rec.Code, rec.Body.String())
		}
	})

	t.Run("missing last_name → 400 binding", func(t *testing.T) {
		orgB, ownerB := newOrgWithOwner(t, ts)
		rec := putProfile(t, ts, bearer(t, ts, ownerB, orgB), userauth.UpdateProfileRequest{FirstName: "OnlyFirst"})
		if rec.Code != http.StatusBadRequest {
			t.Errorf("expected 400, got %d — body: %s", rec.Code, rec.Body.String())
		}
	})

	// A non-empty but whitespace-only name passes the binding `required` tag (the
	// string is non-zero) but is rejected by the service after trimming → 422.
	t.Run("whitespace-only first_name → 422 service validation", func(t *testing.T) {
		orgB, ownerB := newOrgWithOwner(t, ts)
		rec := putProfile(t, ts, bearer(t, ts, ownerB, orgB), userauth.UpdateProfileRequest{
			FirstName: "   ",
			LastName:  "Bloggs",
		})
		if rec.Code != http.StatusUnprocessableEntity {
			t.Errorf("expected 422, got %d — body: %s", rec.Code, rec.Body.String())
		}
	})

	t.Run("unauthenticated → 401", func(t *testing.T) {
		rec := putProfile(t, ts, "", userauth.UpdateProfileRequest{FirstName: "A", LastName: "B"})
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("expected 401, got %d — body: %s", rec.Code, rec.Body.String())
		}
	})
}

// =============================================================================
// PAYROLL FIELDS (NI / UTR / DOB)
// =============================================================================

// TestProfilePayrollFields covers the optional payroll-identity fields on the
// self profile: a happy round-trip + DB persistence, normalisation (NINO upper
// + space-strip), clearing a field to NULL, and the validation 422s.
func TestProfilePayrollFields(t *testing.T) {
	t.Parallel()
	ts := newTestServer(t)
	t.Cleanup(func() { ts.pool.Close() })

	t.Run("set + normalise → round-trips, persists", func(t *testing.T) {
		orgB, ownerB := newOrgWithOwner(t, ts)
		authHeader := bearer(t, ts, ownerB, orgB)

		rec := putProfile(t, ts, authHeader, userauth.UpdateProfileRequest{
			FirstName:               "Aydin",
			LastName:                "Gunal",
			NationalInsuranceNumber: ptr("sy 59 85 39 d"), // lower + spaced
			UTR:                     ptr("1901746095"),
			DateOfBirth:             ptr("1982-04-21"),
		})
		if rec.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d — body: %s", rec.Code, rec.Body.String())
		}
		got := decodeProfile(t, rec.Body.Bytes())
		// NINO is normalised to upper-case, no spaces.
		if got.NationalInsuranceNumber == nil || *got.NationalInsuranceNumber != "SY598539D" {
			t.Errorf("nino: got %v, want SY598539D", got.NationalInsuranceNumber)
		}
		if got.UTR == nil || *got.UTR != "1901746095" {
			t.Errorf("utr: got %v, want 1901746095", got.UTR)
		}
		if got.DateOfBirth == nil || *got.DateOfBirth != "1982-04-21" {
			t.Errorf("dob: got %v, want 1982-04-21", got.DateOfBirth)
		}

		// Persisted to the DB.
		var ni, utr, dob string
		if err := ts.pool.QueryRow(context.Background(),
			`SELECT national_insurance_number, utr, date_of_birth::text FROM users WHERE id = $1`, ownerB).
			Scan(&ni, &utr, &dob); err != nil {
			t.Fatalf("re-read: %v", err)
		}
		if ni != "SY598539D" || utr != "1901746095" || dob != "1982-04-21" {
			t.Errorf("DB mismatch: ni=%q utr=%q dob=%q", ni, utr, dob)
		}
	})

	t.Run("blank clears the column to NULL", func(t *testing.T) {
		orgB, ownerB := newOrgWithOwner(t, ts)
		authHeader := bearer(t, ts, ownerB, orgB)
		// Seed something first.
		if _, err := ts.pool.Exec(context.Background(),
			`UPDATE users SET national_insurance_number = 'SY598539D' WHERE id = $1`, ownerB); err != nil {
			t.Fatalf("seed: %v", err)
		}
		rec := putProfile(t, ts, authHeader, userauth.UpdateProfileRequest{
			FirstName:               "A",
			LastName:                "B",
			NationalInsuranceNumber: ptr("  "),
		})
		if rec.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d — body: %s", rec.Code, rec.Body.String())
		}
		got := decodeProfile(t, rec.Body.Bytes())
		if got.NationalInsuranceNumber != nil {
			t.Errorf("nino should be cleared, got %v", *got.NationalInsuranceNumber)
		}
	})

	t.Run("invalid NINO → 422", func(t *testing.T) {
		orgB, ownerB := newOrgWithOwner(t, ts)
		rec := putProfile(t, ts, bearer(t, ts, ownerB, orgB), userauth.UpdateProfileRequest{
			FirstName: "A", LastName: "B", NationalInsuranceNumber: ptr("12AB"),
		})
		if rec.Code != http.StatusUnprocessableEntity {
			t.Errorf("expected 422, got %d — body: %s", rec.Code, rec.Body.String())
		}
	})

	t.Run("invalid UTR → 422", func(t *testing.T) {
		orgB, ownerB := newOrgWithOwner(t, ts)
		rec := putProfile(t, ts, bearer(t, ts, ownerB, orgB), userauth.UpdateProfileRequest{
			FirstName: "A", LastName: "B", UTR: ptr("123"),
		})
		if rec.Code != http.StatusUnprocessableEntity {
			t.Errorf("expected 422, got %d — body: %s", rec.Code, rec.Body.String())
		}
	})

	t.Run("future date of birth → 422", func(t *testing.T) {
		orgB, ownerB := newOrgWithOwner(t, ts)
		rec := putProfile(t, ts, bearer(t, ts, ownerB, orgB), userauth.UpdateProfileRequest{
			FirstName: "A", LastName: "B", DateOfBirth: ptr("2999-01-01"),
		})
		if rec.Code != http.StatusUnprocessableEntity {
			t.Errorf("expected 422, got %d — body: %s", rec.Code, rec.Body.String())
		}
	})

	// Personal/home address: free text, all optional. Round-trips + persists, and a
	// blank clears the column to NULL (no validation cases — it's free text).
	t.Run("address round-trips, persists, clears", func(t *testing.T) {
		orgB, ownerB := newOrgWithOwner(t, ts)
		authHeader := bearer(t, ts, ownerB, orgB)

		rec := putProfile(t, ts, authHeader, userauth.UpdateProfileRequest{
			FirstName:    "Aydin",
			LastName:     "Gunal",
			AddressLine1: ptr("26 Effra Road"),
			AddressLine2: ptr("London"),
			Postcode:     ptr("SW2 1BZ"),
		})
		if rec.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d — body: %s", rec.Code, rec.Body.String())
		}
		got := decodeProfile(t, rec.Body.Bytes())
		if got.AddressLine1 == nil || *got.AddressLine1 != "26 Effra Road" {
			t.Errorf("address_line_1: got %v, want 26 Effra Road", got.AddressLine1)
		}
		if got.AddressLine2 == nil || *got.AddressLine2 != "London" {
			t.Errorf("address_line_2: got %v, want London", got.AddressLine2)
		}
		if got.Postcode == nil || *got.Postcode != "SW2 1BZ" {
			t.Errorf("postcode: got %v, want SW2 1BZ", got.Postcode)
		}
		// Lines 3/4 were not sent → NULL.
		if got.AddressLine3 != nil || got.AddressLine4 != nil {
			t.Errorf("unset lines should be nil, got l3=%v l4=%v", got.AddressLine3, got.AddressLine4)
		}

		// Persisted to the DB.
		var l1, pc string
		if err := ts.pool.QueryRow(context.Background(),
			`SELECT address_line_1, postcode FROM users WHERE id = $1`, ownerB).Scan(&l1, &pc); err != nil {
			t.Fatalf("re-read: %v", err)
		}
		if l1 != "26 Effra Road" || pc != "SW2 1BZ" {
			t.Errorf("DB mismatch: l1=%q pc=%q", l1, pc)
		}

		// Omitting a field (nil) clears it to NULL — this is how the SPA clears a
		// field (its orNull helper sends null for a blank input). A full-form PUT
		// that doesn't carry address_line_1 NULLs the column.
		rec = putProfile(t, ts, authHeader, userauth.UpdateProfileRequest{
			FirstName: "Aydin", LastName: "Gunal", // address fields omitted → nil → NULL
		})
		if rec.Code != http.StatusOK {
			t.Fatalf("clear: expected 200, got %d — body: %s", rec.Code, rec.Body.String())
		}
		if got = decodeProfile(t, rec.Body.Bytes()); got.AddressLine1 != nil {
			t.Errorf("address_line_1 should be cleared, got %v", *got.AddressLine1)
		}
	})
}

// =============================================================================
// ORGANISATION SWITCHER (GET /me/organisations + POST /me/organisations/switch)
// =============================================================================
//
// A user who belongs to several organisations lists the ones they can switch to
// and re-scopes their session to another. These hit the real DB via the shared
// harness; the switch re-mints a PASETO token, so we decode it to prove the new
// scope. Coverage: list returns every active membership with the right role; a
// switch to a belonged org re-mints a token scoped to it (and is authorised as
// non-owner too); a switch to an org the user does NOT actively belong to is 403
// (non-member, cross-tenant, and a suspended membership); plus the 400/401 edges.

// addMembershipTo attaches an EXISTING user to an org with the given role/status
// (used to build multi-org scenarios) and registers cleanup for that row.
func addMembershipTo(t *testing.T, ts *testServer, orgID, userID, role, status string) {
	t.Helper()
	ctx := context.Background()
	if _, err := ts.pool.Exec(ctx,
		`INSERT INTO organisation_memberships (organisation_id, user_id, role, status)
		 VALUES ($1, $2, $3, $4)`, orgID, userID, role, status); err != nil {
		t.Fatalf("addMembershipTo: %v", err)
	}
	t.Cleanup(func() {
		_, _ = ts.pool.Exec(ctx,
			`DELETE FROM organisation_memberships WHERE organisation_id = $1 AND user_id = $2`, orgID, userID)
	})
}

// newBareOrg creates an organisation with NO memberships (for the "not a member"
// path) and registers cleanup.
func newBareOrg(t *testing.T, ts *testServer) string {
	t.Helper()
	ctx := context.Background()
	orgID := uuid.NewString()
	if _, err := ts.pool.Exec(ctx,
		`INSERT INTO organisations (id, name) VALUES ($1, $2)`, orgID, "Bare Org "+orgID); err != nil {
		t.Fatalf("newBareOrg: %v", err)
	}
	t.Cleanup(func() {
		_, _ = ts.pool.Exec(ctx, `DELETE FROM organisation_memberships WHERE organisation_id = $1`, orgID)
		_, _ = ts.pool.Exec(ctx, `DELETE FROM organisations WHERE id = $1`, orgID)
	})
	return orgID
}

// getMyOrgsReq sends GET /api/v1/me/organisations.
func getMyOrgsReq(t *testing.T, ts *testServer, authHeader string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodGet, "/api/v1/me/organisations", nil)
	if authHeader != "" {
		req.Header.Set("Authorization", authHeader)
	}
	ts.server.router.ServeHTTP(rec, req)
	return rec
}

// switchOrgReq sends POST /api/v1/me/organisations/switch with the given org id.
func switchOrgReq(t *testing.T, ts *testServer, authHeader, orgID string) *httptest.ResponseRecorder {
	t.Helper()
	b, _ := json.Marshal(map[string]string{"organisation_id": orgID})
	rec := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodPost, "/api/v1/me/organisations/switch", bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	if authHeader != "" {
		req.Header.Set("Authorization", authHeader)
	}
	ts.server.router.ServeHTTP(rec, req)
	return rec
}

// TestListMyOrganisations covers GET /api/v1/me/organisations.
func TestListMyOrganisations(t *testing.T) {
	t.Parallel()
	ts := newTestServer(t)
	t.Cleanup(func() { ts.pool.Close() })

	t.Run("returns every active membership with its role", func(t *testing.T) {
		// The user owns orgA (via newOrgWithOwner) and is a plain member of orgB.
		orgA, user := newOrgWithOwner(t, ts)
		orgB := newBareOrg(t, ts)
		addMembershipTo(t, ts, orgB, user, "member", "active")

		// The token is scoped to orgA, but the list is by USER, so orgB shows too.
		rec := getMyOrgsReq(t, ts, bearer(t, ts, user, orgA))
		if rec.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d — body: %s", rec.Code, rec.Body.String())
		}
		var resp struct {
			Organisations []userauth.OrganisationResponse `json:"organisations"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
			t.Fatalf("decode: %v — body: %s", err, rec.Body.String())
		}
		roleByOrg := map[string]string{}
		for _, o := range resp.Organisations {
			roleByOrg[o.ID] = o.Role
		}
		if roleByOrg[orgA] != "owner" {
			t.Errorf("orgA role: got %q, want owner (present=%v)", roleByOrg[orgA], resp.Organisations)
		}
		if roleByOrg[orgB] != "member" {
			t.Errorf("orgB role: got %q, want member", roleByOrg[orgB])
		}
	})

	t.Run("excludes a suspended membership", func(t *testing.T) {
		orgA, user := newOrgWithOwner(t, ts)
		orgB := newBareOrg(t, ts)
		addMembershipTo(t, ts, orgB, user, "member", "suspended")

		rec := getMyOrgsReq(t, ts, bearer(t, ts, user, orgA))
		if rec.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d — body: %s", rec.Code, rec.Body.String())
		}
		var resp struct {
			Organisations []userauth.OrganisationResponse `json:"organisations"`
		}
		_ = json.Unmarshal(rec.Body.Bytes(), &resp)
		for _, o := range resp.Organisations {
			if o.ID == orgB {
				t.Errorf("suspended orgB should not be listed, got %v", resp.Organisations)
			}
		}
	})

	t.Run("unauthenticated → 401", func(t *testing.T) {
		rec := getMyOrgsReq(t, ts, "")
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("expected 401, got %d — body: %s", rec.Code, rec.Body.String())
		}
	})
}

// TestSwitchOrganisation covers POST /api/v1/me/organisations/switch.
func TestSwitchOrganisation(t *testing.T) {
	t.Parallel()
	ts := newTestServer(t)
	t.Cleanup(func() { ts.pool.Close() })

	t.Run("switch to a belonged org re-mints a token scoped to it", func(t *testing.T) {
		orgA, user := newOrgWithOwner(t, ts)
		orgB := newBareOrg(t, ts)
		addMembershipTo(t, ts, orgB, user, "member", "active")

		// Signed in scoped to orgA; switch to orgB.
		rec := switchOrgReq(t, ts, bearer(t, ts, user, orgA), orgB)
		if rec.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d — body: %s", rec.Code, rec.Body.String())
		}
		var resp userauth.LoginUserResponse
		if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
			t.Fatalf("decode: %v — body: %s", err, rec.Body.String())
		}
		if resp.Organisation == nil || resp.Organisation.ID != orgB {
			t.Fatalf("organisation: got %+v, want id %s", resp.Organisation, orgB)
		}
		// Role in orgB is 'member' (per-org), not 'owner' as in orgA.
		if resp.Organisation.Role != "member" {
			t.Errorf("role: got %q, want member", resp.Organisation.Role)
		}
		// The new token must actually be scoped to orgB — decode it and check.
		payload, err := ts.tokenMaker.VerifyToken(resp.AccessToken)
		if err != nil {
			t.Fatalf("verify re-minted token: %v", err)
		}
		if payload.OrganisationID.String() != orgB {
			t.Errorf("token org: got %s, want %s", payload.OrganisationID, orgB)
		}
		if payload.UserID.String() != user {
			t.Errorf("token user: got %s, want %s", payload.UserID, user)
		}
	})

	t.Run("switch to an org the user is NOT a member of → 403", func(t *testing.T) {
		orgA, user := newOrgWithOwner(t, ts)
		other := newBareOrg(t, ts) // exists, but user has no membership

		rec := switchOrgReq(t, ts, bearer(t, ts, user, orgA), other)
		if rec.Code != http.StatusForbidden {
			t.Errorf("expected 403, got %d — body: %s", rec.Code, rec.Body.String())
		}
	})

	t.Run("switch to a suspended membership → 403", func(t *testing.T) {
		orgA, user := newOrgWithOwner(t, ts)
		orgB := newBareOrg(t, ts)
		addMembershipTo(t, ts, orgB, user, "member", "suspended")

		rec := switchOrgReq(t, ts, bearer(t, ts, user, orgA), orgB)
		if rec.Code != http.StatusForbidden {
			t.Errorf("expected 403, got %d — body: %s", rec.Code, rec.Body.String())
		}
	})

	t.Run("malformed organisation_id → 400", func(t *testing.T) {
		orgA, user := newOrgWithOwner(t, ts)
		rec := switchOrgReq(t, ts, bearer(t, ts, user, orgA), "not-a-uuid")
		if rec.Code != http.StatusBadRequest {
			t.Errorf("expected 400, got %d — body: %s", rec.Code, rec.Body.String())
		}
	})

	t.Run("unauthenticated → 401", func(t *testing.T) {
		orgB := newBareOrg(t, ts)
		rec := switchOrgReq(t, ts, "", orgB)
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("expected 401, got %d — body: %s", rec.Code, rec.Body.String())
		}
	})
}
