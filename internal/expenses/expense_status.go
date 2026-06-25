package expenses

// expense_status.go
// =============================================================================
// The expense approval-workflow STATE MACHINE.
//
// An expense's `status` column moves through a small, deliberate lifecycle.
// This file is the single place that lifecycle is written down — as data (a
// transition table) plus one service method that drives it. Keeping the whole
// machine in one readable place matters more here than cleverness.
//
//             submit                approve
//    DRAFT ───────────▶ SUBMITTED ───────────▶ APPROVED   (terminal for now)
//      ▲                    │
//      │ reopen             │ reject
//      └──────── REJECTED ◀─┘
//
//   - SUBMITTED is a lock-in: there is no "withdraw" back to DRAFT.
//   - APPROVED is terminal for this change (PAID is out of scope — see BACKLOG).
//   - Fixing a rejected expense is two steps: reopen (→ DRAFT), edit, submit.
//     That dovetails with the existing rule that DRAFT and REJECTED are the
//     only EDITABLE states (UpdateExpense / DeleteExpense).
//
// `status` (the approval lifecycle) is a separate axis from `needs_review` (the
// Smart-Upload data-capture lifecycle); this machine touches only `status`.
// =============================================================================

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	expensesdb "github.com/operationfb/accounting-saas/db/expenses"
	kernel "github.com/operationfb/accounting-saas/internal/kernel"
)

// Status constants for the expensesdb.status column. These finally replace the
// bare "DRAFT"/"REJECTED" string literals that were scattered through the
// editability checks, so a typo is now a compile error rather than a silent bug.
const (
	StatusDraft     = "DRAFT"
	StatusSubmitted = "SUBMITTED"
	StatusApproved  = "APPROVED"
	StatusRejected  = "REJECTED"
	StatusPaid      = "PAID" // not yet reachable via a transition — see BACKLOG
)

// transition describes one legal move of the state machine. The target status
// is NOT stored here: each transition has its own dedicated SQL query
// (SubmitExpense / ApproveExpense / ...) that hardcodes the destination, so the
// query is the single source of truth for "what it becomes". This struct
// captures the two things the SERVICE must check before calling that query:
// the required starting status, and who is allowed to make the move.
type transition struct {
	from      string // the status the expense MUST currently be in
	adminOnly bool   // true → owner/admin only; false → the claimant (own) or an owner/admin
	needsNote bool   // true → a rejection_note is required (only reject)
}

// statusTransitions is the entire state machine, keyed by the action name the
// API accepts (POST /expenses/:id/status {"action": ...}). Adding a transition
// is a one-line change here plus its dedicated query.
var statusTransitions = map[string]transition{
	"submit":  {from: StatusDraft, adminOnly: false},
	"approve": {from: StatusSubmitted, adminOnly: true},
	"reject":  {from: StatusSubmitted, adminOnly: true, needsNote: true},
	"reopen":  {from: StatusRejected, adminOnly: false},
}

// ChangeExpenseStatus performs one approval-workflow transition on an expense.
//
// The flow mirrors UpdateExpense deliberately: cheap input checks first, then a
// single transaction that loads the row, authorises the caller, enforces the
// state machine, and writes — all together so the row cannot change status
// between the check and the write (closing the TOCTOU gap).
//
//	action        — one of "submit" / "approve" / "reject" / "reopen"
//	rejectionNote — required only when action == "reject"
func (s *Service) ChangeExpenseStatus(
	ctx context.Context,
	authUserID uuid.UUID,
	authOrgID uuid.UUID,
	id string,
	action string,
	rejectionNote string,
) (*ExpenseResponse, error) {
	expenseUUID, err := uuid.Parse(id)
	if err != nil {
		return nil, kernel.ErrValidation("id is not a valid UUID", err)
	}

	// Look the action up in the state machine. An unknown action is a validation
	// error (the HTTP binding also rejects it, but the service must stand on its
	// own — it's called directly from tests and could be called elsewhere later).
	t, ok := statusTransitions[action]
	if !ok {
		return nil, kernel.ErrValidation(fmt.Sprintf("unknown status action %q", action), nil)
	}

	// A rejection must carry a reason. Trim so whitespace-only doesn't count.
	if t.needsNote && strings.TrimSpace(rejectionNote) == "" {
		return nil, kernel.ErrValidation("rejection_note is required when rejecting an expense", nil)
	}

	// The caller must be an active member of the org; capture their role to gate
	// the admin-only transitions below.
	role, err := s.authorize(ctx, authUserID, authOrgID)
	if err != nil {
		return nil, err
	}

	var updated expensesdb.Expense
	err = s.withTransaction(ctx, func(qtx *expensesdb.Queries) error {
		// Load the current row (org-scoped, so another tenant's id is simply not
		// found → 404, never revealing its existence).
		existing, err := qtx.GetExpense(ctx, expensesdb.GetExpenseParams{
			ID:             expenseUUID,
			OrganisationID: authOrgID,
		})
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return kernel.ErrNotFound("expense", id)
			}
			return kernel.ErrInternal(err)
		}

		// Authorise WHO may act (mirrors UpdateExpense ordering: who, then state).
		// adminOnly transitions (approve/reject) are owner/admin only; the rest
		// (submit/reopen) may be done by the claimant on their own expense, or by
		// an owner/admin on anyone's.
		if t.adminOnly {
			if !kernel.IsOrgAdmin(role) {
				return kernel.ErrForbidden(fmt.Sprintf("only an owner or admin can %s this expense", action))
			}
		} else if existing.UserID != authUserID && !kernel.IsOrgAdmin(role) {
			return kernel.ErrForbidden("you do not have access to this expense")
		}

		// Enforce the state machine: the expense must be in the transition's
		// required `from` status. kernel.ErrCodeConflict (409) is exactly this case —
		// e.g. approving something that isn't SUBMITTED.
		if existing.Status != t.from {
			return kernel.ErrConflict(fmt.Sprintf(
				"cannot %s an expense in %s status (must be %s)", action, existing.Status, t.from))
		}

		// Filed-period lock: approve is the ONLY transition that moves an expense INTO
		// the VAT-counted set (APPROVED), so it can ADD input VAT to a period. If the
		// expense is dated in an already-filed period, approving would change a
		// submitted return after the fact — refuse it. The other transitions
		// (submit/reject/reopen) move only among un-counted states, so they're exempt.
		if action == "approve" {
			if err := s.assertNotFiled(ctx, authOrgID, existing.DatedOn); err != nil {
				return err
			}
		}

		// Apply the transition via its dedicated query. Each query changes only
		// the columns its transition owns (so, e.g., approve preserves submitted_at).
		switch action {
		case "submit":
			updated, err = qtx.SubmitExpense(ctx, expensesdb.SubmitExpenseParams{
				ID:             expenseUUID,
				OrganisationID: authOrgID,
			})
		case "approve":
			updated, err = qtx.ApproveExpense(ctx, expensesdb.ApproveExpenseParams{
				ID:             expenseUUID,
				OrganisationID: authOrgID,
				// uuid.UUID's underlying type is [16]byte, so it assigns straight
				// into pgtype.UUID.Bytes; Valid: true means "not NULL".
				ApprovedByUserID: pgtype.UUID{Bytes: authUserID, Valid: true},
			})
		case "reject":
			updated, err = qtx.RejectExpense(ctx, expensesdb.RejectExpenseParams{
				ID:             expenseUUID,
				OrganisationID: authOrgID,
				RejectionNote:  pgtype.Text{String: rejectionNote, Valid: true},
			})
		case "reopen":
			updated, err = qtx.ReopenExpense(ctx, expensesdb.ReopenExpenseParams{
				ID:             expenseUUID,
				OrganisationID: authOrgID,
			})
		}
		if err != nil {
			return kernel.ErrInternal(err)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	// On a successful APPROVAL, emit the domain event that drives the external
	// FreeAgent push (Pub/Sub → Eventarc → Cloud Workflow). This is the monolith's
	// ENTIRE role in pushing — it knows nothing about FreeAgent beyond "an expense
	// was approved".
	//
	// Best-effort by design: the transaction has already committed, so a publish
	// failure must NOT undo an approval the caller already saw succeed. We log it
	// and move on; a missed event is recoverable via the manual re-push endpoint.
	// (A transactional outbox is the durability upgrade — see BACKLOG.)
	if action == "approve" && s.publisher != nil {
		ev := ExpenseApprovedEvent{
			Event:          EventExpenseApproved,
			OrganisationID: authOrgID,
			ExpenseID:      expenseUUID,
			OccurredAt:     time.Now().UTC(),
		}
		if perr := s.publisher.PublishExpenseApproved(ctx, ev); perr != nil {
			slog.Error("expense approved but event publish failed",
				"expense_id", expenseUUID, "organisation_id", authOrgID, "err", perr)
		}
	}

	return expenseToResponse(updated), nil
}

// RepublishApprovedExpense re-emits the "expense.approved" event for an already
// APPROVED expense — the manual "push to FreeAgent again" action, for when the
// automatic publish on approval was lost or the push failed. It is safe to call
// repeatedly: the workflow's `already_pushed` guard skips an expense that pushed
// successfully and retries one that didn't.
//
// Unlike the best-effort publish on the approval path, this one SURFACES a publish
// failure to the caller (they asked for it explicitly and want to know it worked).
// Owner/admin only; org-scoped; only APPROVED expenses are pushable.
func (s *Service) RepublishApprovedExpense(
	ctx context.Context,
	authUserID uuid.UUID,
	authOrgID uuid.UUID,
	id string,
) error {
	expenseUUID, err := uuid.Parse(id)
	if err != nil {
		return kernel.ErrValidation("id is not a valid UUID", err)
	}

	role, err := s.authorize(ctx, authUserID, authOrgID)
	if err != nil {
		return err
	}
	if !kernel.IsOrgAdmin(role) {
		return kernel.ErrForbidden("only an owner or admin can push an expense to FreeAgent")
	}

	// Load the expense org-scoped (a cross-tenant id is simply not found → 404).
	existing, err := s.queries.GetExpense(ctx, expensesdb.GetExpenseParams{
		ID:             expenseUUID,
		OrganisationID: authOrgID,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return kernel.ErrNotFound("expense", id)
		}
		return kernel.ErrInternal(err)
	}
	if existing.Status != StatusApproved {
		return kernel.ErrConflict("only approved expenses can be pushed to FreeAgent")
	}
	if s.publisher == nil {
		return kernel.ErrConflict("event publishing is not configured")
	}

	ev := ExpenseApprovedEvent{
		Event:          EventExpenseApproved,
		OrganisationID: authOrgID,
		ExpenseID:      expenseUUID,
		OccurredAt:     time.Now().UTC(),
	}
	if err := s.publisher.PublishExpenseApproved(ctx, ev); err != nil {
		return kernel.ErrInternal(err)
	}
	return nil
}
