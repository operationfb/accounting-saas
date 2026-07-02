package tala

// dto.go
// =============================================================================
// Request/response DTOs for the Tala assistant endpoint (POST /api/v1/tala/chat).
//
// The conversation is STATELESS on the server: the SPA keeps the running history
// and sends it with every request. There is no chat-thread table in v1.
// =============================================================================

import "encoding/json"

// ChatMessage is one turn in the conversation as the SPA sends it: a role
// ("user" or "assistant") and plain text content.
type ChatMessage struct {
	Role    string `json:"role"    binding:"required,oneof=user assistant"`
	Content string `json:"content" binding:"required"`
}

// ChatRequest is the body of POST /api/v1/tala/chat — the full conversation so
// far. The final message must be from the user (that's the turn Tala answers).
type ChatRequest struct {
	Messages []ChatMessage `json:"messages" binding:"required,min=1,dive"`
}

// ChatResponse is what Tala returns for one turn.
type ChatResponse struct {
	Reply           string           `json:"reply"`
	ProposedActions []ProposedAction `json:"proposed_actions"`
	// ToolCalls lists the names of the tools Tala invoked this turn — surfaced in
	// the UI for transparency ("Tala checked: list_outstanding_invoices").
	ToolCalls []string `json:"tool_calls"`
}

// ProposedAction is a guarded write Tala wants to make. It is NOT executed by the
// agent loop — the loop is read-only. The SPA renders it as a confirmation card
// and, on the user's explicit Confirm, calls the EXISTING domain endpoint (chosen
// by Kind). This keeps mutations behind the already-tested, already-authorised
// service layer, so a prompt-injection or model mistake can't move money.
type ProposedAction struct {
	Kind    string          `json:"kind"`    // "create_expense" | "approve_expense"
	Title   string          `json:"title"`   // short label for the card
	Summary string          `json:"summary"` // human-readable description of the effect
	Payload json.RawMessage `json:"payload"` // fields the SPA uses to build the confirm request
}
