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
	ts := newTestServer(t)
	defer ts.pool.Close()

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
	ts := newTestServer(t)
	defer ts.pool.Close()

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
