package expenses

// events.go
// =============================================================================
// Domain events the monolith emits for OTHER systems to react to. Today there is
// exactly one — "expense.approved" — which the external FreeAgent push consumes
// (Pub/Sub → Eventarc → Cloud Workflow). Publishing is the monolith's ENTIRE
// runtime responsibility for the push: it does not know about FreeAgent.
//
// EventPublisher is an interface (like Storage / HTMLRenderer) so the publish is a
// pluggable, optional concern: production wires the Pub/Sub implementation
// (events_pubsub.go); tests use a fake; and when no topic is configured the field
// is left nil and publishing is simply skipped.
// =============================================================================

import (
	"context"
	"time"

	"github.com/google/uuid"
)

// EventExpenseApproved is the event-type string carried in the payload and as a
// Pub/Sub message attribute (so a subscription can filter on it).
const EventExpenseApproved = "expense.approved"

// ExpenseApprovedEvent is the payload published when an expense is approved. It
// carries IDs only (not a full snapshot): the workflow fetches the authoritative
// current data from the internal endpoint, so the message stays small and can't go
// stale.
type ExpenseApprovedEvent struct {
	Event          string    `json:"event"` // always EventExpenseApproved
	OrganisationID uuid.UUID `json:"organisation_id"`
	ExpenseID      uuid.UUID `json:"expense_id"`
	OccurredAt     time.Time `json:"occurred_at"`
}

// EventPublisher publishes domain events. A nil EventPublisher means publishing is
// disabled (no topic configured) — callers must nil-check before use.
type EventPublisher interface {
	PublishExpenseApproved(ctx context.Context, e ExpenseApprovedEvent) error
}
