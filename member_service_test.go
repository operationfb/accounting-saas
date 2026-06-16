package main

// member_service_test.go
// =============================================================================
// Integration tests for the members module (GET /api/v1/members).
//
// Like the rest of the suite these hit a REAL PostgreSQL database via the shared
// newTestServer harness (server_test.go) and skip cleanly when DATABASE_URL is
// unset. Ephemeral users/orgs are created directly in the DB and removed in
// t.Cleanup so the shared dev DB stays clean.
//
// Coverage: the owner/admin gate (owner ok, admin ok, plain member 403,
// non-member 403, unauthenticated 401), the shape of the returned rows (joined
// email + role + member_since), and multi-tenant scoping (one org never sees
// another's members).
// =============================================================================

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
)

// =============================================================================
// MEMBER TEST HELPERS
// =============================================================================

// getMembers sends GET /api/v1/members with the given auth header (empty = none),
// returning the recorder.
func getMembers(t *testing.T, ts *testServer, authHeader string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodGet, "/api/v1/members", nil)
	if authHeader != "" {
		req.Header.Set("Authorization", authHeader)
	}
	ts.server.router.ServeHTTP(rec, req)
	return rec
}

// membersFromList decodes { "members": [ ... ] } into a slice.
func membersFromList(t *testing.T, body []byte) []MemberResponse {
	t.Helper()
	var resp struct {
		Members []MemberResponse `json:"members"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		t.Fatalf("membersFromList: decode: %v", err)
	}
	return resp.Members
}

// findMember returns a pointer to the member with the given user id, or nil if
// it is absent from the slice.
func findMember(members []MemberResponse, userID string) *MemberResponse {
	for i := range members {
		if members[i].UserID == userID {
			return &members[i]
		}
	}
	return nil
}

// newAdminUser inserts an ephemeral active 'admin' user into the org (mirrors
// server_test.go's newMemberUser, which is hard-coded to 'member') and registers
// cleanup that removes the membership and the user row. Returns the user's id.
func newAdminUser(t *testing.T, ts *testServer, orgID string) string {
	t.Helper()
	id := uuid.NewString()
	email := "admin-" + id + "@test.local"
	ctx := context.Background()
	if _, err := ts.pool.Exec(ctx,
		`INSERT INTO users (id, email, first_name, last_name, is_active, email_verified_at)
		 VALUES ($1, $2, 'Test', 'Admin', TRUE, now())`, id, email); err != nil {
		t.Fatalf("newAdminUser: insert user: %v", err)
	}
	if _, err := ts.pool.Exec(ctx,
		`INSERT INTO organisation_memberships (organisation_id, user_id, role, status)
		 VALUES ($1, $2, 'admin', 'active')`, orgID, id); err != nil {
		t.Fatalf("newAdminUser: insert membership: %v", err)
	}
	t.Cleanup(func() {
		_, _ = ts.pool.Exec(ctx, `DELETE FROM organisation_memberships WHERE user_id = $1`, id)
		_, _ = ts.pool.Exec(ctx, `DELETE FROM users WHERE id = $1`, id)
	})
	return id
}

// =============================================================================
// TESTS
// =============================================================================

// TestHandleListMembers covers GET /api/v1/members end-to-end through the router.
func TestHandleListMembers(t *testing.T) {
	ts := newTestServer(t)
	defer ts.pool.Close()

	t.Run("owner sees the org's members", func(t *testing.T) {
		// Add an ephemeral member to the dev org so the list has more than the
		// seeded owner(s) in it.
		memberID := newMemberUser(t, ts, devOrgID)

		rec := getMembers(t, ts, bearer(t, ts, devUserID, devOrgID))
		if rec.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d — body: %s", rec.Code, rec.Body.String())
		}
		members := membersFromList(t, rec.Body.Bytes())

		// The seeded dev user is an OWNER and must appear, with the joined user
		// fields populated (proves the membership↔user join works).
		owner := findMember(members, devUserID)
		if owner == nil {
			t.Fatalf("members list does not contain the dev owner %s", devUserID)
		}
		if owner.Role != "owner" {
			t.Errorf("dev user role: got %q, want %q", owner.Role, "owner")
		}
		if owner.Email == "" {
			t.Error("owner.email is empty — the users join did not populate it")
		}
		if owner.MemberSince == "" {
			t.Error("owner.member_since is empty — expected an RFC3339 timestamp")
		}

		// The ephemeral member must appear too, as a 'member'.
		m := findMember(members, memberID)
		if m == nil {
			t.Fatalf("members list does not contain the ephemeral member %s", memberID)
		}
		if m.Role != "member" {
			t.Errorf("ephemeral member role: got %q, want %q", m.Role, "member")
		}
	})

	t.Run("admin is allowed", func(t *testing.T) {
		adminID := newAdminUser(t, ts, devOrgID)

		rec := getMembers(t, ts, bearer(t, ts, adminID, devOrgID))
		if rec.Code != http.StatusOK {
			t.Fatalf("expected 200 for an admin, got %d — body: %s", rec.Code, rec.Body.String())
		}
		if findMember(membersFromList(t, rec.Body.Bytes()), adminID) == nil {
			t.Errorf("admin %s not present in their own org's members list", adminID)
		}
	})

	t.Run("plain member is forbidden", func(t *testing.T) {
		memberID := newMemberUser(t, ts, devOrgID)

		rec := getMembers(t, ts, bearer(t, ts, memberID, devOrgID))
		if rec.Code != http.StatusForbidden {
			t.Fatalf("expected 403 for a plain member, got %d — body: %s", rec.Code, rec.Body.String())
		}
	})

	t.Run("non-member is forbidden", func(t *testing.T) {
		// A validly-signed token for a user with no membership in the dev org.
		stranger := uuid.NewString()

		rec := getMembers(t, ts, bearer(t, ts, stranger, devOrgID))
		if rec.Code != http.StatusForbidden {
			t.Fatalf("expected 403 for a non-member, got %d — body: %s", rec.Code, rec.Body.String())
		}
	})

	t.Run("unauthenticated is rejected", func(t *testing.T) {
		rec := getMembers(t, ts, "")
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("expected 401 without a token, got %d — body: %s", rec.Code, rec.Body.String())
		}
	})

	t.Run("tenant isolation: an owner sees only their own org", func(t *testing.T) {
		// A separate org with its own single owner.
		orgB, ownerB := newOrgWithOwner(t, ts)
		// And an ephemeral member in the DEV org, which orgB must never see.
		devMember := newMemberUser(t, ts, devOrgID)

		rec := getMembers(t, ts, bearer(t, ts, ownerB, orgB))
		if rec.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d — body: %s", rec.Code, rec.Body.String())
		}
		members := membersFromList(t, rec.Body.Bytes())

		// orgB was created with exactly one member: ownerB.
		if findMember(members, ownerB) == nil {
			t.Errorf("orgB owner %s missing from orgB's own members list", ownerB)
		}
		if len(members) != 1 {
			t.Errorf("orgB should have exactly 1 member, got %d: %+v", len(members), members)
		}
		// None of the dev org's members may leak into orgB's list.
		if findMember(members, devUserID) != nil {
			t.Errorf("tenant leak: dev owner %s appeared in orgB's members list", devUserID)
		}
		if findMember(members, devMember) != nil {
			t.Errorf("tenant leak: dev member %s appeared in orgB's members list", devMember)
		}
	})
}
