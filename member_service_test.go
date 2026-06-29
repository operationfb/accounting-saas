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
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"

	members "github.com/operationfb/accounting-saas/internal/members"
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
func membersFromList(t *testing.T, body []byte) []members.MemberResponse {
	t.Helper()
	var resp struct {
		Members []members.MemberResponse `json:"members"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		t.Fatalf("membersFromList: decode: %v", err)
	}
	return resp.Members
}

// findMember returns a pointer to the member with the given user id, or nil if
// it is absent from the slice.
func findMember(members []members.MemberResponse, userID string) *members.MemberResponse {
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

// getMemberReq sends GET /api/v1/members/:id.
func getMemberReq(t *testing.T, ts *testServer, authHeader, id string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodGet, "/api/v1/members/"+id, nil)
	if authHeader != "" {
		req.Header.Set("Authorization", authHeader)
	}
	ts.server.router.ServeHTTP(rec, req)
	return rec
}

// putMemberReq sends PUT /api/v1/members/:id with a JSON body.
func putMemberReq(t *testing.T, ts *testServer, authHeader, id string, body members.UpdateMemberRequest) *httptest.ResponseRecorder {
	t.Helper()
	bodyBytes, _ := json.Marshal(body)
	rec := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodPut, "/api/v1/members/"+id, bytes.NewReader(bodyBytes))
	req.Header.Set("Content-Type", "application/json")
	if authHeader != "" {
		req.Header.Set("Authorization", authHeader)
	}
	ts.server.router.ServeHTTP(rec, req)
	return rec
}

// decodeMemberDetail decodes the bare MemberDetailResponse the handler returns.
func decodeMemberDetail(t *testing.T, body []byte) members.MemberDetailResponse {
	t.Helper()
	var resp members.MemberDetailResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		t.Fatalf("decodeMemberDetail: %v — body: %s", err, string(body))
	}
	return resp
}

// =============================================================================
// TESTS
// =============================================================================

// TestHandleGetMember covers GET /api/v1/members/:id (the admin User Details read).
func TestHandleGetMember(t *testing.T) {
	ts := newTestServer(t)
	defer ts.pool.Close()

	t.Run("admin reads another member's detail incl payroll fields", func(t *testing.T) {
		orgID, ownerID := newOrgWithOwner(t, ts)
		targetID := newMemberUser(t, ts, orgID)
		// Seed payroll fields on the target so we can prove GET returns them.
		if _, err := ts.pool.Exec(context.Background(),
			`UPDATE users SET national_insurance_number = 'SY598539D', utr = '1901746095', date_of_birth = '1982-04-21' WHERE id = $1`,
			targetID); err != nil {
			t.Fatalf("seed payroll: %v", err)
		}

		rec := getMemberReq(t, ts, bearer(t, ts, ownerID, orgID), targetID)
		if rec.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d — body: %s", rec.Code, rec.Body.String())
		}
		got := decodeMemberDetail(t, rec.Body.Bytes())
		if got.UserID != targetID {
			t.Errorf("user_id: got %q, want %q", got.UserID, targetID)
		}
		if got.Role != "member" || got.Status != "active" {
			t.Errorf("role/status: got %q/%q, want member/active", got.Role, got.Status)
		}
		if got.NationalInsuranceNumber == nil || *got.NationalInsuranceNumber != "SY598539D" {
			t.Errorf("nino: got %v, want SY598539D", got.NationalInsuranceNumber)
		}
		if got.UTR == nil || *got.UTR != "1901746095" {
			t.Errorf("utr: got %v, want 1901746095", got.UTR)
		}
		if got.DateOfBirth == nil || *got.DateOfBirth != "1982-04-21" {
			t.Errorf("date_of_birth: got %v, want 1982-04-21", got.DateOfBirth)
		}
	})

	t.Run("plain member is forbidden", func(t *testing.T) {
		orgID, _ := newOrgWithOwner(t, ts)
		caller := newMemberUser(t, ts, orgID)
		target := newMemberUser(t, ts, orgID)

		rec := getMemberReq(t, ts, bearer(t, ts, caller, orgID), target)
		if rec.Code != http.StatusForbidden {
			t.Fatalf("expected 403, got %d — body: %s", rec.Code, rec.Body.String())
		}
	})

	t.Run("cross-tenant target → 404", func(t *testing.T) {
		orgA, ownerA := newOrgWithOwner(t, ts)
		orgB, _ := newOrgWithOwner(t, ts)
		// A user that belongs to orgB only.
		strangerInB := newMemberUser(t, ts, orgB)

		rec := getMemberReq(t, ts, bearer(t, ts, ownerA, orgA), strangerInB)
		if rec.Code != http.StatusNotFound {
			t.Fatalf("expected 404, got %d — body: %s", rec.Code, rec.Body.String())
		}
	})

	t.Run("malformed id → 400", func(t *testing.T) {
		orgID, ownerID := newOrgWithOwner(t, ts)
		rec := getMemberReq(t, ts, bearer(t, ts, ownerID, orgID), "not-a-uuid")
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("expected 400, got %d — body: %s", rec.Code, rec.Body.String())
		}
	})

	t.Run("unauthenticated → 401", func(t *testing.T) {
		orgID, _ := newOrgWithOwner(t, ts)
		_ = orgID
		rec := getMemberReq(t, ts, "", uuid.NewString())
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("expected 401, got %d — body: %s", rec.Code, rec.Body.String())
		}
	})
}

// TestHandleUpdateMember covers PUT /api/v1/members/:id (the admin User Details edit).
func TestHandleUpdateMember(t *testing.T) {
	ts := newTestServer(t)
	defer ts.pool.Close()

	t.Run("admin updates details + payroll + role + status → persists", func(t *testing.T) {
		orgID, ownerID := newOrgWithOwner(t, ts)
		targetID := newMemberUser(t, ts, orgID)

		rec := putMemberReq(t, ts, bearer(t, ts, ownerID, orgID), targetID, members.UpdateMemberRequest{
			FirstName:               "Aydin",
			LastName:                "Gunal",
			NationalInsuranceNumber: ptr("SY598539D"),
			UTR:                     ptr("1901746095"),
			DateOfBirth:             ptr("1982-04-21"),
			AddressLine1:            ptr("26 Effra Road"),
			AddressLine2:            ptr("London"),
			Postcode:                ptr("SW2 1BZ"),
			Role:                    "admin",
			Status:                  "suspended",
		})
		if rec.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d — body: %s", rec.Code, rec.Body.String())
		}
		got := decodeMemberDetail(t, rec.Body.Bytes())
		if got.FirstName != "Aydin" || got.LastName != "Gunal" {
			t.Errorf("name: got %q %q", got.FirstName, got.LastName)
		}
		if got.Role != "admin" || got.Status != "suspended" {
			t.Errorf("role/status: got %q/%q, want admin/suspended", got.Role, got.Status)
		}
		if got.AddressLine1 == nil || *got.AddressLine1 != "26 Effra Road" ||
			got.Postcode == nil || *got.Postcode != "SW2 1BZ" {
			t.Errorf("address: got line1=%v postcode=%v", got.AddressLine1, got.Postcode)
		}

		// Committed to the DB (users + membership).
		var ni, utr, dob, addr1, postcode, role, status string
		if err := ts.pool.QueryRow(context.Background(),
			`SELECT u.national_insurance_number, u.utr, u.date_of_birth::text,
			        u.address_line_1, u.postcode, m.role, m.status
			 FROM users u JOIN organisation_memberships m ON m.user_id = u.id
			 WHERE u.id = $1 AND m.organisation_id = $2`, targetID, orgID).
			Scan(&ni, &utr, &dob, &addr1, &postcode, &role, &status); err != nil {
			t.Fatalf("re-read: %v", err)
		}
		if ni != "SY598539D" || utr != "1901746095" || dob != "1982-04-21" {
			t.Errorf("payroll not persisted: ni=%q utr=%q dob=%q", ni, utr, dob)
		}
		if addr1 != "26 Effra Road" || postcode != "SW2 1BZ" {
			t.Errorf("address not persisted: line1=%q postcode=%q", addr1, postcode)
		}
		if role != "admin" || status != "suspended" {
			t.Errorf("membership not persisted: role=%q status=%q", role, status)
		}
	})

	t.Run("invalid NI number → 422", func(t *testing.T) {
		orgID, ownerID := newOrgWithOwner(t, ts)
		targetID := newMemberUser(t, ts, orgID)
		rec := putMemberReq(t, ts, bearer(t, ts, ownerID, orgID), targetID, members.UpdateMemberRequest{
			FirstName:               "X",
			LastName:                "Y",
			NationalInsuranceNumber: ptr("NOPE"),
			Role:                    "member",
			Status:                  "active",
		})
		if rec.Code != http.StatusUnprocessableEntity {
			t.Fatalf("expected 422, got %d — body: %s", rec.Code, rec.Body.String())
		}
	})

	t.Run("invalid role value → 400 binding", func(t *testing.T) {
		orgID, ownerID := newOrgWithOwner(t, ts)
		targetID := newMemberUser(t, ts, orgID)
		rec := putMemberReq(t, ts, bearer(t, ts, ownerID, orgID), targetID, members.UpdateMemberRequest{
			FirstName: "X", LastName: "Y", Role: "superuser", Status: "active",
		})
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("expected 400, got %d — body: %s", rec.Code, rec.Body.String())
		}
	})

	t.Run("plain member caller → 403", func(t *testing.T) {
		orgID, _ := newOrgWithOwner(t, ts)
		caller := newMemberUser(t, ts, orgID)
		target := newMemberUser(t, ts, orgID)
		rec := putMemberReq(t, ts, bearer(t, ts, caller, orgID), target, members.UpdateMemberRequest{
			FirstName: "X", LastName: "Y", Role: "member", Status: "active",
		})
		if rec.Code != http.StatusForbidden {
			t.Fatalf("expected 403, got %d — body: %s", rec.Code, rec.Body.String())
		}
	})

	t.Run("self role/status change is forbidden", func(t *testing.T) {
		orgID, ownerID := newOrgWithOwner(t, ts)
		// Owner edits THEMSELVES, trying to change status → blocked.
		rec := putMemberReq(t, ts, bearer(t, ts, ownerID, orgID), ownerID, members.UpdateMemberRequest{
			FirstName: "Self", LastName: "Edit", Role: "owner", Status: "suspended",
		})
		if rec.Code != http.StatusForbidden {
			t.Fatalf("expected 403, got %d — body: %s", rec.Code, rec.Body.String())
		}
	})

	t.Run("only an owner may assign the owner role", func(t *testing.T) {
		orgID, _ := newOrgWithOwner(t, ts)
		admin := newAdminUser(t, ts, orgID)
		target := newMemberUser(t, ts, orgID)
		// An admin promoting a member to owner → blocked.
		rec := putMemberReq(t, ts, bearer(t, ts, admin, orgID), target, members.UpdateMemberRequest{
			FirstName: "X", LastName: "Y", Role: "owner", Status: "active",
		})
		if rec.Code != http.StatusForbidden {
			t.Fatalf("expected 403, got %d — body: %s", rec.Code, rec.Body.String())
		}
	})

	t.Run("admin may not modify an existing owner", func(t *testing.T) {
		orgID, ownerID := newOrgWithOwner(t, ts)
		admin := newAdminUser(t, ts, orgID)
		// An admin editing the org owner → blocked (target's current role is owner).
		rec := putMemberReq(t, ts, bearer(t, ts, admin, orgID), ownerID, members.UpdateMemberRequest{
			FirstName: "Owner", LastName: "Renamed", Role: "owner", Status: "active",
		})
		if rec.Code != http.StatusForbidden {
			t.Fatalf("expected 403, got %d — body: %s", rec.Code, rec.Body.String())
		}
	})

	t.Run("cross-tenant target → 404", func(t *testing.T) {
		orgA, ownerA := newOrgWithOwner(t, ts)
		orgB, _ := newOrgWithOwner(t, ts)
		strangerInB := newMemberUser(t, ts, orgB)
		rec := putMemberReq(t, ts, bearer(t, ts, ownerA, orgA), strangerInB, members.UpdateMemberRequest{
			FirstName: "X", LastName: "Y", Role: "member", Status: "active",
		})
		if rec.Code != http.StatusNotFound {
			t.Fatalf("expected 404, got %d — body: %s", rec.Code, rec.Body.String())
		}
	})
}

// postMemberReq sends POST /api/v1/members with a JSON body. When the response is
// 201, it decodes the created member and registers t.Cleanup to delete the new
// user + membership so the shared dev DB stays clean.
func postMemberReq(t *testing.T, ts *testServer, authHeader string, body members.CreateMemberRequest) *httptest.ResponseRecorder {
	t.Helper()
	bodyBytes, _ := json.Marshal(body)
	rec := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodPost, "/api/v1/members", bytes.NewReader(bodyBytes))
	req.Header.Set("Content-Type", "application/json")
	if authHeader != "" {
		req.Header.Set("Authorization", authHeader)
	}
	ts.server.router.ServeHTTP(rec, req)
	if rec.Code == http.StatusCreated {
		created := decodeMemberDetail(t, rec.Body.Bytes())
		t.Cleanup(func() {
			ctx := context.Background()
			_, _ = ts.pool.Exec(ctx, `DELETE FROM organisation_memberships WHERE user_id = $1`, created.UserID)
			_, _ = ts.pool.Exec(ctx, `DELETE FROM users WHERE id = $1`, created.UserID)
		})
	}
	return rec
}

// loginCode sends POST /api/v1/auth/login and returns the status code (used to
// prove the admin-set password actually works for the new user).
func loginCode(t *testing.T, ts *testServer, email, password string) int {
	t.Helper()
	b, _ := json.Marshal(map[string]string{"email": email, "password": password})
	rec := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodPost, "/api/v1/auth/login", bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	ts.server.router.ServeHTTP(rec, req)
	return rec.Code
}

// TestHandleCreateMember covers POST /api/v1/members (an owner/admin adding a new
// user to the organisation with an initial password).
func TestHandleCreateMember(t *testing.T) {
	ts := newTestServer(t)
	defer ts.pool.Close()

	t.Run("owner creates a user → active membership + can log in", func(t *testing.T) {
		orgID, ownerID := newOrgWithOwner(t, ts)
		email := "new-" + uuid.NewString() + "@test.local"

		rec := postMemberReq(t, ts, bearer(t, ts, ownerID, orgID), members.CreateMemberRequest{
			Email:     email,
			Password:  "supersecret1",
			FirstName: "New",
			LastName:  "User",
			Role:      "member",
		})
		if rec.Code != http.StatusCreated {
			t.Fatalf("expected 201, got %d — body: %s", rec.Code, rec.Body.String())
		}
		got := decodeMemberDetail(t, rec.Body.Bytes())
		if got.Email != email || got.FirstName != "New" || got.LastName != "User" {
			t.Errorf("response fields: %+v", got)
		}
		if got.Role != "member" || got.Status != "active" {
			t.Errorf("role/status: got %q/%q, want member/active", got.Role, got.Status)
		}

		// Active membership row exists in this org.
		var role, status string
		if err := ts.pool.QueryRow(context.Background(),
			`SELECT role, status FROM organisation_memberships WHERE user_id = $1 AND organisation_id = $2`,
			got.UserID, orgID).Scan(&role, &status); err != nil {
			t.Fatalf("re-read membership: %v", err)
		}
		if role != "member" || status != "active" {
			t.Errorf("membership not persisted: role=%q status=%q", role, status)
		}

		// The admin-set password actually works (bcrypt round-trip through login).
		if code := loginCode(t, ts, email, "supersecret1"); code != http.StatusOK {
			t.Errorf("new user login: got %d, want 200", code)
		}

		// And the new user shows up in the members list.
		list := membersFromList(t, getMembers(t, ts, bearer(t, ts, ownerID, orgID)).Body.Bytes())
		if findMember(list, got.UserID) == nil {
			t.Errorf("new user %s missing from members list", got.UserID)
		}
	})

	t.Run("plain member caller → 403", func(t *testing.T) {
		orgID, _ := newOrgWithOwner(t, ts)
		caller := newMemberUser(t, ts, orgID)
		rec := postMemberReq(t, ts, bearer(t, ts, caller, orgID), members.CreateMemberRequest{
			Email: "x-" + uuid.NewString() + "@test.local", Password: "supersecret1",
			FirstName: "X", LastName: "Y", Role: "member",
		})
		if rec.Code != http.StatusForbidden {
			t.Fatalf("expected 403, got %d — body: %s", rec.Code, rec.Body.String())
		}
	})

	t.Run("duplicate email → 409", func(t *testing.T) {
		orgID, ownerID := newOrgWithOwner(t, ts)
		email := "dup-" + uuid.NewString() + "@test.local"
		auth := bearer(t, ts, ownerID, orgID)

		first := postMemberReq(t, ts, auth, members.CreateMemberRequest{
			Email: email, Password: "supersecret1", FirstName: "First", LastName: "User", Role: "member",
		})
		if first.Code != http.StatusCreated {
			t.Fatalf("setup create: expected 201, got %d — body: %s", first.Code, first.Body.String())
		}
		second := postMemberReq(t, ts, auth, members.CreateMemberRequest{
			Email: email, Password: "supersecret1", FirstName: "Second", LastName: "User", Role: "member",
		})
		if second.Code != http.StatusConflict {
			t.Fatalf("expected 409, got %d — body: %s", second.Code, second.Body.String())
		}
	})

	t.Run("owner role is rejected at binding → 400", func(t *testing.T) {
		orgID, ownerID := newOrgWithOwner(t, ts)
		rec := postMemberReq(t, ts, bearer(t, ts, ownerID, orgID), members.CreateMemberRequest{
			Email: "o-" + uuid.NewString() + "@test.local", Password: "supersecret1",
			FirstName: "X", LastName: "Y", Role: "owner",
		})
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("expected 400, got %d — body: %s", rec.Code, rec.Body.String())
		}
	})

	t.Run("short password → 400", func(t *testing.T) {
		orgID, ownerID := newOrgWithOwner(t, ts)
		rec := postMemberReq(t, ts, bearer(t, ts, ownerID, orgID), members.CreateMemberRequest{
			Email: "p-" + uuid.NewString() + "@test.local", Password: "short",
			FirstName: "X", LastName: "Y", Role: "member",
		})
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("expected 400, got %d — body: %s", rec.Code, rec.Body.String())
		}
	})

	t.Run("unauthenticated → 401", func(t *testing.T) {
		rec := postMemberReq(t, ts, "", members.CreateMemberRequest{
			Email: "u-" + uuid.NewString() + "@test.local", Password: "supersecret1",
			FirstName: "X", LastName: "Y", Role: "member",
		})
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("expected 401, got %d — body: %s", rec.Code, rec.Body.String())
		}
	})

	t.Run("tenant isolation: created member belongs only to caller's org", func(t *testing.T) {
		orgA, ownerA := newOrgWithOwner(t, ts)
		orgB, ownerB := newOrgWithOwner(t, ts)

		rec := postMemberReq(t, ts, bearer(t, ts, ownerA, orgA), members.CreateMemberRequest{
			Email: "iso-" + uuid.NewString() + "@test.local", Password: "supersecret1",
			FirstName: "Iso", LastName: "Lated", Role: "member",
		})
		if rec.Code != http.StatusCreated {
			t.Fatalf("expected 201, got %d — body: %s", rec.Code, rec.Body.String())
		}
		newUserID := decodeMemberDetail(t, rec.Body.Bytes()).UserID

		// orgB must never see orgA's new member.
		listB := membersFromList(t, getMembers(t, ts, bearer(t, ts, ownerB, orgB)).Body.Bytes())
		if findMember(listB, newUserID) != nil {
			t.Errorf("tenant leak: new member %s appeared in orgB's list", newUserID)
		}
	})
}

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

// TestMemberPayroll covers the admin-only nested payroll block on /members/:id:
// a round-trip with money→pence + DB persistence, the defaults served when no row
// exists yet, and the money/enum validation 422s. The owner/admin gate itself is
// already covered by TestHandleGetMember / TestHandleUpdateMember (payroll rides the
// same methods).
func TestMemberPayroll(t *testing.T) {
	ts := newTestServer(t)
	defer ts.pool.Close()

	t.Run("admin sets payroll → round-trips, persists as pence", func(t *testing.T) {
		orgID, ownerID := newOrgWithOwner(t, ts)
		targetID := newMemberUser(t, ts, orgID)

		rec := putMemberReq(t, ts, bearer(t, ts, ownerID, orgID), targetID, members.UpdateMemberRequest{
			FirstName: "Test", LastName: "Member", Role: "member", Status: "active",
			Payroll: &members.PayrollDTO{
				StartDate:                ptr("2026-05-04"),
				StartingDeclaration:      ptr("C"),
				NicCalculation:           "employee",
				PaidIrregularly:          true,
				PayrollID:                ptr("EMP01"),
				TaxCode:                  ptr("2207L"),
				NiCategoryLetter:         "A",
				StudentLoanUndergraduate: true,
				BasicPay:                 "700.00",
				OtherDeductionsNetPay:    "12.50",
				PensionStatus:            "opted_out_or_ineligible",
				LeavingNextPayRun:        true,
				LeavingDate:              ptr("2026-07-31"),
			},
		})
		if rec.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d — body: %s", rec.Code, rec.Body.String())
		}
		got := decodeMemberDetail(t, rec.Body.Bytes())
		if got.Payroll == nil {
			t.Fatalf("response has no payroll block")
		}
		if got.Payroll.BasicPay != "700.00" {
			t.Errorf("basic_pay: got %q, want 700.00", got.Payroll.BasicPay)
		}
		if got.Payroll.OtherDeductionsNetPay != "12.50" {
			t.Errorf("other_deductions_net_pay: got %q, want 12.50", got.Payroll.OtherDeductionsNetPay)
		}
		if got.Payroll.TaxCode == nil || *got.Payroll.TaxCode != "2207L" {
			t.Errorf("tax_code: got %v, want 2207L", got.Payroll.TaxCode)
		}
		if !got.Payroll.PaidIrregularly || !got.Payroll.StudentLoanUndergraduate {
			t.Errorf("booleans not round-tripped: %+v", got.Payroll)
		}
		if got.Payroll.LeavingDate == nil || *got.Payroll.LeavingDate != "2026-07-31" {
			t.Errorf("leaving_date: got %v, want 2026-07-31", got.Payroll.LeavingDate)
		}

		// Persisted as pence in the DB.
		var basic, deductions int64
		if err := ts.pool.QueryRow(context.Background(),
			`SELECT basic_pay_minor, other_deductions_net_pay_minor FROM employee_payroll
			 WHERE organisation_id = $1 AND user_id = $2`, orgID, targetID).
			Scan(&basic, &deductions); err != nil {
			t.Fatalf("re-read: %v", err)
		}
		if basic != 70000 || deductions != 1250 {
			t.Errorf("pence mismatch: basic=%d (want 70000), deductions=%d (want 1250)", basic, deductions)
		}
	})

	t.Run("GET with no payroll row → defaults", func(t *testing.T) {
		orgID, ownerID := newOrgWithOwner(t, ts)
		targetID := newMemberUser(t, ts, orgID)

		rec := getMemberReq(t, ts, bearer(t, ts, ownerID, orgID), targetID)
		if rec.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d — body: %s", rec.Code, rec.Body.String())
		}
		got := decodeMemberDetail(t, rec.Body.Bytes())
		if got.Payroll == nil {
			t.Fatalf("payroll should be present with defaults, got nil")
		}
		if got.Payroll.BasicPay != "0.00" || got.Payroll.NicCalculation != "employee" ||
			got.Payroll.NiCategoryLetter != "A" || got.Payroll.PensionStatus != "opted_out_or_ineligible" {
			t.Errorf("unexpected defaults: %+v", got.Payroll)
		}
	})

	t.Run("invalid money amount → 422", func(t *testing.T) {
		orgID, ownerID := newOrgWithOwner(t, ts)
		targetID := newMemberUser(t, ts, orgID)
		rec := putMemberReq(t, ts, bearer(t, ts, ownerID, orgID), targetID, members.UpdateMemberRequest{
			FirstName: "T", LastName: "M", Role: "member", Status: "active",
			Payroll: &members.PayrollDTO{BasicPay: "notmoney"},
		})
		if rec.Code != http.StatusUnprocessableEntity {
			t.Fatalf("expected 422, got %d — body: %s", rec.Code, rec.Body.String())
		}
	})

	t.Run("invalid enum value → 422", func(t *testing.T) {
		orgID, ownerID := newOrgWithOwner(t, ts)
		targetID := newMemberUser(t, ts, orgID)
		rec := putMemberReq(t, ts, bearer(t, ts, ownerID, orgID), targetID, members.UpdateMemberRequest{
			FirstName: "T", LastName: "M", Role: "member", Status: "active",
			Payroll: &members.PayrollDTO{NicCalculation: "bogus"},
		})
		if rec.Code != http.StatusUnprocessableEntity {
			t.Fatalf("expected 422, got %d — body: %s", rec.Code, rec.Body.String())
		}
	})
}
