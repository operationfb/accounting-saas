package main

// expense_status_test.go
// =============================================================================
// Integration tests for the approval-workflow state machine:
//
//	POST /api/v1/expenses/:id/status  {"action": ..., "rejection_note": ...}
//
// Real Postgres, like the rest of server_test.go. Reuses newTestServer, bearer,
// createExpenseAs, newMemberUser, newOrgWithOwner, decodeExpense, mustUUID,
// assertAppCode, devUserID, devOrgID. Non-DRAFT preconditions are arranged by
// walking the machine through the endpoint (so the happy-path sequence is
// itself exercised) or, where a shortcut is clearer, with direct SQL — mirroring
// how TestHandleUpdateExpense forces a non-editable status.
// =============================================================================

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"
)

// =============================================================================
// HELPERS
// =============================================================================

// postStatus sends POST /api/v1/expenses/:id/status with the given auth header
// (empty = none) and body, returning the recorder.
func postStatus(t *testing.T, ts *testServer, id, authHeader string, body ChangeExpenseStatusRequest) *httptest.ResponseRecorder {
	t.Helper()
	bodyBytes, _ := json.Marshal(body)
	rec := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodPost, "/api/v1/expenses/"+id+"/status", bytes.NewReader(bodyBytes))
	req.Header.Set("Content-Type", "application/json")
	if authHeader != "" {
		req.Header.Set("Authorization", authHeader)
	}
	ts.server.router.ServeHTTP(rec, req)
	return rec
}

// setExpenseStatus forces an expense into a status via direct SQL, to arrange a
// precondition without walking the machine (mirrors TestHandleUpdateExpense).
func setExpenseStatus(t *testing.T, ts *testServer, id, status string) {
	t.Helper()
	if _, err := ts.pool.Exec(context.Background(),
		"UPDATE expenses SET status = $2 WHERE id = $1", id, status); err != nil {
		t.Fatalf("setExpenseStatus(%s): %v", status, err)
	}
}

// workflowCols holds the approval-workflow columns for DB assertions. The
// nullable ones are pointers so a NULL reads back as nil (approved_by_user_id is
// cast to text so it scans straight into *string).
type workflowCols struct {
	status        string
	submittedAt   *time.Time
	approvedAt    *time.Time
	approvedBy    *string
	rejectionNote *string
}

func readWorkflowCols(t *testing.T, ts *testServer, id string) workflowCols {
	t.Helper()
	var c workflowCols
	if err := ts.pool.QueryRow(context.Background(),
		`SELECT status, submitted_at, approved_at, approved_by_user_id::text, rejection_note
		   FROM expenses WHERE id = $1`, id).
		Scan(&c.status, &c.submittedAt, &c.approvedAt, &c.approvedBy, &c.rejectionNote); err != nil {
		t.Fatalf("readWorkflowCols: %v", err)
	}
	return c
}

// submittedExpenseByMember creates a fresh active member, has them create a
// DRAFT and submit it, and returns (memberID, expenseID) sitting in SUBMITTED.
// newMemberUser's t.Cleanup removes the expense afterwards.
func submittedExpenseByMember(t *testing.T, ts *testServer) (memberID, expenseID string) {
	t.Helper()
	memberID = newMemberUser(t, ts, devOrgID)
	expenseID = createExpenseAs(t, ts, memberID, devOrgID)
	rec := postStatus(t, ts, expenseID, bearer(t, ts, memberID, devOrgID), ChangeExpenseStatusRequest{Action: "submit"})
	if rec.Code != http.StatusOK {
		t.Fatalf("arrange submit: expected 200, got %d — body: %s", rec.Code, rec.Body.String())
	}
	return memberID, expenseID
}

// =============================================================================
// TESTS
// =============================================================================

// TestHandleChangeExpenseStatus covers the happy-path transitions and their
// column effects, the illegal transitions (409), authorization (403 / positive
// admin case), request validation (400 binding / 422 service guard), auth (401),
// and multi-tenant isolation (404).
func TestHandleChangeExpenseStatus(t *testing.T) {
	ts := newTestServer(t)
	defer ts.pool.Close()

	devAuth := bearer(t, ts, devUserID, devOrgID) // the dev user is an org owner (admin)

	// ---- Happy paths + column effects -------------------------------------

	t.Run("submit: claimant submits own draft → SUBMITTED + submitted_at", func(t *testing.T) {
		member := newMemberUser(t, ts, devOrgID)
		id := createExpenseAs(t, ts, member, devOrgID)

		rec := postStatus(t, ts, id, bearer(t, ts, member, devOrgID), ChangeExpenseStatusRequest{Action: "submit"})
		if rec.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d — body: %s", rec.Code, rec.Body.String())
		}
		if got := decodeExpense(t, rec).Status; got != StatusSubmitted {
			t.Errorf("response status: got %q, want %q", got, StatusSubmitted)
		}
		cols := readWorkflowCols(t, ts, id)
		if cols.status != StatusSubmitted {
			t.Errorf("db status: got %q, want %q", cols.status, StatusSubmitted)
		}
		if cols.submittedAt == nil {
			t.Error("submitted_at should be set after submit")
		}
	})

	t.Run("approve: admin approves submitted → APPROVED, approver+time set, submitted_at preserved", func(t *testing.T) {
		_, id := submittedExpenseByMember(t, ts)
		before := readWorkflowCols(t, ts, id)
		if before.submittedAt == nil {
			t.Fatal("precondition: submitted_at should be set after submit")
		}

		rec := postStatus(t, ts, id, devAuth, ChangeExpenseStatusRequest{Action: "approve"})
		if rec.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d — body: %s", rec.Code, rec.Body.String())
		}
		if got := decodeExpense(t, rec).Status; got != StatusApproved {
			t.Errorf("response status: got %q, want %q", got, StatusApproved)
		}

		after := readWorkflowCols(t, ts, id)
		if after.status != StatusApproved {
			t.Errorf("db status: got %q, want %q", after.status, StatusApproved)
		}
		if after.approvedAt == nil {
			t.Error("approved_at should be set after approve")
		}
		if after.approvedBy == nil || *after.approvedBy != devUserID {
			t.Errorf("approved_by_user_id: got %v, want %q", after.approvedBy, devUserID)
		}
		// The regression the dedicated query exists to prevent: approve must NOT
		// wipe submitted_at (the old single UpdateExpenseStatus would have).
		if after.submittedAt == nil {
			t.Error("submitted_at must be preserved across approve")
		} else if !after.submittedAt.Equal(*before.submittedAt) {
			t.Errorf("submitted_at changed across approve: before=%v after=%v", before.submittedAt, after.submittedAt)
		}
	})

	t.Run("reject: admin rejects submitted with note → REJECTED, note stored, submitted_at preserved", func(t *testing.T) {
		_, id := submittedExpenseByMember(t, ts)
		before := readWorkflowCols(t, ts, id)

		const note = "Missing VAT receipt — please attach and resubmit."
		rec := postStatus(t, ts, id, devAuth, ChangeExpenseStatusRequest{Action: "reject", RejectionNote: note})
		if rec.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d — body: %s", rec.Code, rec.Body.String())
		}
		if got := decodeExpense(t, rec).Status; got != StatusRejected {
			t.Errorf("response status: got %q, want %q", got, StatusRejected)
		}

		after := readWorkflowCols(t, ts, id)
		if after.status != StatusRejected {
			t.Errorf("db status: got %q, want %q", after.status, StatusRejected)
		}
		if after.rejectionNote == nil || *after.rejectionNote != note {
			t.Errorf("rejection_note: got %v, want %q", after.rejectionNote, note)
		}
		if before.submittedAt == nil || after.submittedAt == nil || !after.submittedAt.Equal(*before.submittedAt) {
			t.Error("submitted_at must be preserved across reject")
		}
	})

	t.Run("reopen: rejected → DRAFT, workflow metadata cleared", func(t *testing.T) {
		member, id := submittedExpenseByMember(t, ts)
		// Walk to REJECTED first (admin rejects).
		if rec := postStatus(t, ts, id, devAuth, ChangeExpenseStatusRequest{Action: "reject", RejectionNote: "fix it"}); rec.Code != http.StatusOK {
			t.Fatalf("arrange reject: expected 200, got %d — body: %s", rec.Code, rec.Body.String())
		}
		// The claimant reopens their own rejected expense.
		rec := postStatus(t, ts, id, bearer(t, ts, member, devOrgID), ChangeExpenseStatusRequest{Action: "reopen"})
		if rec.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d — body: %s", rec.Code, rec.Body.String())
		}
		if got := decodeExpense(t, rec).Status; got != StatusDraft {
			t.Errorf("response status: got %q, want %q", got, StatusDraft)
		}
		cols := readWorkflowCols(t, ts, id)
		if cols.status != StatusDraft {
			t.Errorf("db status: got %q, want %q", cols.status, StatusDraft)
		}
		if cols.submittedAt != nil || cols.approvedAt != nil || cols.approvedBy != nil || cols.rejectionNote != nil {
			t.Errorf("reopen must clear submitted_at/approved_at/approved_by/rejection_note, got %+v", cols)
		}
	})

	// ---- Illegal transitions → 409 ----------------------------------------

	t.Run("illegal transitions return 409", func(t *testing.T) {
		cases := []struct {
			name   string
			setup  string // status to force via SQL first; "" leaves it DRAFT
			action string
		}{
			{"approve a DRAFT", "", "approve"},
			{"reject a DRAFT", "", "reject"},
			{"reopen a DRAFT", "", "reopen"},
			{"submit a SUBMITTED", StatusSubmitted, "submit"},
			{"reopen a SUBMITTED", StatusSubmitted, "reopen"},
			{"submit an APPROVED", StatusApproved, "submit"},
			{"approve an APPROVED", StatusApproved, "approve"},
			{"reopen an APPROVED", StatusApproved, "reopen"},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				id := createExpenseAs(t, ts, devUserID, devOrgID)
				if tc.setup != "" {
					setExpenseStatus(t, ts, id, tc.setup)
				}
				body := ChangeExpenseStatusRequest{Action: tc.action}
				if tc.action == "reject" {
					body.RejectionNote = "n/a" // satisfy binding so we reach the state check
				}
				rec := postStatus(t, ts, id, devAuth, body)
				if rec.Code != http.StatusConflict {
					t.Errorf("expected 409, got %d — body: %s", rec.Code, rec.Body.String())
				}
			})
		}
	})

	// ---- Authorization → 403 (and a positive admin case) ------------------

	t.Run("non-admin claimant cannot approve own submitted → 403", func(t *testing.T) {
		member, id := submittedExpenseByMember(t, ts)
		rec := postStatus(t, ts, id, bearer(t, ts, member, devOrgID), ChangeExpenseStatusRequest{Action: "approve"})
		if rec.Code != http.StatusForbidden {
			t.Errorf("expected 403, got %d — body: %s", rec.Code, rec.Body.String())
		}
	})

	t.Run("non-admin claimant cannot reject own submitted → 403", func(t *testing.T) {
		member, id := submittedExpenseByMember(t, ts)
		rec := postStatus(t, ts, id, bearer(t, ts, member, devOrgID), ChangeExpenseStatusRequest{Action: "reject", RejectionNote: "self-reject?"})
		if rec.Code != http.StatusForbidden {
			t.Errorf("expected 403, got %d — body: %s", rec.Code, rec.Body.String())
		}
	})

	t.Run("member cannot submit another user's draft → 403", func(t *testing.T) {
		ownerDraft := createExpenseAs(t, ts, devUserID, devOrgID)
		member := newMemberUser(t, ts, devOrgID)
		rec := postStatus(t, ts, ownerDraft, bearer(t, ts, member, devOrgID), ChangeExpenseStatusRequest{Action: "submit"})
		if rec.Code != http.StatusForbidden {
			t.Errorf("expected 403, got %d — body: %s", rec.Code, rec.Body.String())
		}
	})

	t.Run("owner/admin can approve a member's submitted expense → 200", func(t *testing.T) {
		_, id := submittedExpenseByMember(t, ts)
		rec := postStatus(t, ts, id, devAuth, ChangeExpenseStatusRequest{Action: "approve"})
		if rec.Code != http.StatusOK {
			t.Errorf("expected 200, got %d — body: %s", rec.Code, rec.Body.String())
		}
	})

	// ---- Validation: binding (400) and service guard (422) ----------------

	t.Run("reject without note → 400 (binding)", func(t *testing.T) {
		_, id := submittedExpenseByMember(t, ts)
		rec := postStatus(t, ts, id, devAuth, ChangeExpenseStatusRequest{Action: "reject"})
		if rec.Code != http.StatusBadRequest {
			t.Errorf("expected 400, got %d — body: %s", rec.Code, rec.Body.String())
		}
	})

	t.Run("reject with whitespace-only note → 422 (service guard)", func(t *testing.T) {
		_, id := submittedExpenseByMember(t, ts)
		// A single space passes the `required_if` binding (it's non-empty) but the
		// service trims it and rejects → 422, exercising the service-layer guard
		// that exists independently of the HTTP binding.
		rec := postStatus(t, ts, id, devAuth, ChangeExpenseStatusRequest{Action: "reject", RejectionNote: "   "})
		if rec.Code != http.StatusUnprocessableEntity {
			t.Errorf("expected 422, got %d — body: %s", rec.Code, rec.Body.String())
		}
	})

	t.Run("unknown action → 400 (binding)", func(t *testing.T) {
		id := createExpenseAs(t, ts, devUserID, devOrgID)
		rec := postStatus(t, ts, id, devAuth, ChangeExpenseStatusRequest{Action: "frobnicate"})
		if rec.Code != http.StatusBadRequest {
			t.Errorf("expected 400, got %d — body: %s", rec.Code, rec.Body.String())
		}
	})

	// ---- Auth required → 401 ----------------------------------------------

	t.Run("requires auth → 401", func(t *testing.T) {
		rec := postStatus(t, ts, uuid.NewString(), "", ChangeExpenseStatusRequest{Action: "submit"})
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("expected 401, got %d — body: %s", rec.Code, rec.Body.String())
		}
	})

	// ---- Multi-tenant isolation → 404 -------------------------------------

	t.Run("another org cannot transition this org's expense → 404", func(t *testing.T) {
		expenseA := createExpenseAs(t, ts, devUserID, devOrgID)
		orgB, userB := newOrgWithOwner(t, ts)
		// Org B's owner is an active admin in their own org (so authorize passes),
		// but the expense isn't in org B's scope → 404 (existence not revealed).
		rec := postStatus(t, ts, expenseA, bearer(t, ts, userB, orgB), ChangeExpenseStatusRequest{Action: "submit"})
		if rec.Code != http.StatusNotFound {
			t.Errorf("cross-tenant: expected 404, got %d — body: %s", rec.Code, rec.Body.String())
		}
	})

	t.Run("unknown id → 404", func(t *testing.T) {
		rec := postStatus(t, ts, uuid.NewString(), devAuth, ChangeExpenseStatusRequest{Action: "submit"})
		if rec.Code != http.StatusNotFound {
			t.Errorf("expected 404, got %d — body: %s", rec.Code, rec.Body.String())
		}
	})
}

// TestChangeExpenseStatusServiceGuards exercises the service-layer validation
// directly (not through the HTTP binding). The service must stand on its own:
// it is called from tests and could be wired to a non-HTTP caller later, so its
// guards on the id, the action, and the rejection note are checked here too.
// These return before any DB work, so the placeholder ids need not exist.
func TestChangeExpenseStatusServiceGuards(t *testing.T) {
	ts := newTestServer(t)
	defer ts.pool.Close()

	ctx := context.Background()
	caller, org := mustUUID(t, devUserID), mustUUID(t, devOrgID)

	t.Run("bad UUID → validation error", func(t *testing.T) {
		_, err := ts.server.expenseService.ChangeExpenseStatus(ctx, caller, org, "not-a-uuid", "submit", "")
		assertAppCode(t, err, ErrCodeValidation)
	})

	t.Run("unknown action → validation error", func(t *testing.T) {
		_, err := ts.server.expenseService.ChangeExpenseStatus(ctx, caller, org, uuid.NewString(), "frobnicate", "")
		assertAppCode(t, err, ErrCodeValidation)
	})

	t.Run("reject with empty note → validation error", func(t *testing.T) {
		_, err := ts.server.expenseService.ChangeExpenseStatus(ctx, caller, org, uuid.NewString(), "reject", "")
		assertAppCode(t, err, ErrCodeValidation)
	})
}
