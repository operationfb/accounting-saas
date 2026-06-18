package main

// events_test.go
// =============================================================================
// Tests for the "expense.approved" publish trigger (Phase 3 / B1).
//
// Pub/Sub itself is an external service, so it is faked (fakeEventPublisher); the
// rest runs against the real DB via the shared harness. We assert three things:
// approving publishes exactly one event with the right ids; non-approve
// transitions publish nothing; and a publish FAILURE does not fail the approval
// (it's best-effort — the row still commits).
// =============================================================================

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

// fakeEventPublisher records published events and can be told to return an error.
// The publish happens synchronously inside ChangeExpenseStatus, so no locking is
// needed for these single-threaded tests.
type fakeEventPublisher struct {
	events []ExpenseApprovedEvent
	err    error
}

func (f *fakeEventPublisher) PublishExpenseApproved(_ context.Context, e ExpenseApprovedEvent) error {
	f.events = append(f.events, e)
	return f.err
}

func TestExpenseApproved_PublishesEvent(t *testing.T) {
	ts := newTestServer(t)
	defer ts.pool.Close()

	devAuth := bearer(t, ts, devUserID, devOrgID) // dev user is an owner/admin → may approve

	t.Run("approve publishes one event with the right ids", func(t *testing.T) {
		fake := &fakeEventPublisher{}
		ts.server.expenseService.publisher = fake

		_, expenseID := submittedExpenseByMember(t, ts)
		rec := postStatus(t, ts, expenseID, devAuth, ChangeExpenseStatusRequest{Action: "approve"})
		if rec.Code != http.StatusOK {
			t.Fatalf("approve: expected 200, got %d — body: %s", rec.Code, rec.Body.String())
		}

		if len(fake.events) != 1 {
			t.Fatalf("expected exactly 1 published event, got %d", len(fake.events))
		}
		ev := fake.events[0]
		if ev.Event != eventExpenseApproved {
			t.Errorf("event type: got %q, want %q", ev.Event, eventExpenseApproved)
		}
		if ev.ExpenseID.String() != expenseID {
			t.Errorf("expense id: got %s, want %s", ev.ExpenseID, expenseID)
		}
		if ev.OrganisationID.String() != devOrgID {
			t.Errorf("organisation id: got %s, want %s", ev.OrganisationID, devOrgID)
		}
		if ev.OccurredAt.IsZero() {
			t.Error("occurred_at should be set")
		}
	})

	t.Run("non-approve transitions publish nothing", func(t *testing.T) {
		fake := &fakeEventPublisher{}
		ts.server.expenseService.publisher = fake

		// submit is a transition, but only approve publishes.
		member := newMemberUser(t, ts, devOrgID)
		id := createExpenseAs(t, ts, member, devOrgID)
		rec := postStatus(t, ts, id, bearer(t, ts, member, devOrgID), ChangeExpenseStatusRequest{Action: "submit"})
		if rec.Code != http.StatusOK {
			t.Fatalf("submit: expected 200, got %d — body: %s", rec.Code, rec.Body.String())
		}
		if len(fake.events) != 0 {
			t.Errorf("submit must not publish; got %d events", len(fake.events))
		}
	})

	t.Run("publish failure does not fail the approval (best-effort)", func(t *testing.T) {
		fake := &fakeEventPublisher{err: errors.New("pubsub unavailable")}
		ts.server.expenseService.publisher = fake

		_, expenseID := submittedExpenseByMember(t, ts)
		rec := postStatus(t, ts, expenseID, devAuth, ChangeExpenseStatusRequest{Action: "approve"})
		if rec.Code != http.StatusOK {
			t.Fatalf("approve with a failing publisher: expected 200 (best-effort), got %d — body: %s", rec.Code, rec.Body.String())
		}
		// The approval still committed despite the publish error.
		if cols := readWorkflowCols(t, ts, expenseID); cols.status != StatusApproved {
			t.Errorf("status after approve: got %q, want %q", cols.status, StatusApproved)
		}
	})
}

// postRepush hits POST /api/v1/integrations/freeagent/expenses/:id/push.
func postRepush(t *testing.T, ts *testServer, id, authHeader string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodPost, "/api/v1/integrations/freeagent/expenses/"+id+"/push", nil)
	if authHeader != "" {
		req.Header.Set("Authorization", authHeader)
	}
	ts.server.router.ServeHTTP(rec, req)
	return rec
}

func TestRepushApprovedExpense(t *testing.T) {
	ts := newTestServer(t)
	defer ts.pool.Close()

	devAuth := bearer(t, ts, devUserID, devOrgID) // owner/admin

	t.Run("admin re-pushes an approved expense → 202 + one more event", func(t *testing.T) {
		fake := &fakeEventPublisher{}
		ts.server.expenseService.publisher = fake

		_, expenseID := submittedExpenseByMember(t, ts)
		if rec := postStatus(t, ts, expenseID, devAuth, ChangeExpenseStatusRequest{Action: "approve"}); rec.Code != http.StatusOK {
			t.Fatalf("approve: expected 200, got %d — body: %s", rec.Code, rec.Body.String())
		}
		before := len(fake.events) // 1, from the approval's auto-publish

		rec := postRepush(t, ts, expenseID, devAuth)
		if rec.Code != http.StatusAccepted {
			t.Fatalf("re-push: expected 202, got %d — body: %s", rec.Code, rec.Body.String())
		}
		if len(fake.events) != before+1 {
			t.Errorf("re-push should publish one more event: before=%d, after=%d", before, len(fake.events))
		}
	})

	t.Run("non-approved (DRAFT) expense → 409", func(t *testing.T) {
		ts.server.expenseService.publisher = &fakeEventPublisher{}
		member := newMemberUser(t, ts, devOrgID)
		id := createExpenseAs(t, ts, member, devOrgID) // DRAFT
		rec := postRepush(t, ts, id, devAuth)
		if rec.Code != http.StatusConflict {
			t.Errorf("re-push DRAFT: expected 409, got %d — body: %s", rec.Code, rec.Body.String())
		}
	})

	t.Run("non-admin → 403", func(t *testing.T) {
		ts.server.expenseService.publisher = &fakeEventPublisher{}
		member := newMemberUser(t, ts, devOrgID)
		id := createExpenseAs(t, ts, member, devOrgID)
		rec := postRepush(t, ts, id, bearer(t, ts, member, devOrgID))
		if rec.Code != http.StatusForbidden {
			t.Errorf("member re-push: expected 403, got %d — body: %s", rec.Code, rec.Body.String())
		}
	})
}
