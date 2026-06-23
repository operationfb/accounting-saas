package invoices

// status.go
// =============================================================================
// The invoice STATUS lifecycle as data — the small state machine the ChangeStatus
// endpoint drives. Mirrors internal/expenses/expense_status.go (status-as-data).
//
// Only the EXPLICIT, user-driven states are stored; these constants mirror the
// CHECK constraint in db/schema/invoices_schema.sql. The display states a user
// sees (Open / Overdue / Paid / Overpaid / Zero Value) are DERIVED at read time in
// service.go (deriveDisplayStatus), never stored, so they can't go stale.
//
//             issue                 write_off
//    DRAFT ───────────▶ SENT ──────────────────▶ WRITTEN_OFF
//      │ │                │ ▲          refund
//      │ │ schedule       │ └──────────────────▶ REFUNDED
//      │ ▼     send       │
//      SCHEDULED ─────────┘
//      ▲                  │
//      └──── reopen ◀──────┘   (reopen: SCHEDULED|SENT → DRAFT)
// =============================================================================

// Stored lifecycle states. Keep in sync with the invoices.status CHECK constraint.
const (
	StatusDraft      = "DRAFT"
	StatusScheduled  = "SCHEDULED"
	StatusSent       = "SENT"
	StatusWrittenOff = "WRITTEN_OFF"
	StatusRefunded   = "REFUNDED"
)

// statusTransition is one allowed move: the set of states an action may run FROM,
// and the single state it moves TO.
type statusTransition struct {
	from []string
	to   string
}

// invoiceActions maps the API `action` discriminator to its allowed transition.
// The handler's `oneof` binding rejects any action NOT in this set (400); the
// service then checks the invoice's CURRENT status is in `from` (else 409).
//
// NOTE: the ChangeStatusRequest `oneof` tag in dto.go must list exactly these keys
// — struct tags can't reference a constant, so keep them in step by hand.
var invoiceActions = map[string]statusTransition{
	"issue":     {from: []string{StatusDraft}, to: StatusSent},
	"schedule":  {from: []string{StatusDraft}, to: StatusScheduled},
	"send":      {from: []string{StatusScheduled}, to: StatusSent},
	"write_off": {from: []string{StatusSent}, to: StatusWrittenOff},
	"refund":    {from: []string{StatusSent}, to: StatusRefunded},
	"reopen":    {from: []string{StatusScheduled, StatusSent}, to: StatusDraft},
}

// resolveTransition returns the target status for (action, current). ok is false
// when the action is legal in general but NOT from the current status (→ 409). An
// unknown action never reaches here — the handler's binding rejects it first.
func resolveTransition(action, current string) (target string, ok bool) {
	t, found := invoiceActions[action]
	if !found {
		return "", false
	}
	for _, f := range t.from {
		if f == current {
			return t.to, true
		}
	}
	return "", false
}
