package tala

// service_test.go
// =============================================================================
// Unit tests for the Tala agent loop. The ONLY thing mocked is the external LLM
// (via the modelClient seam) — everything Tala itself does is exercised for real:
// tool dispatch, org/user pass-through, tool-error recovery, proposal collection
// (with no mutation), the iteration cap, and history validation.
//
// These tests deliberately do NOT wire the real domain services. Real-Postgres,
// cross-tenant isolation is already covered by each domain's own suite; Tala's
// job — and what these tests assert — is that it hands each tool exactly the
// token's userID/orgID (never model-supplied) and never mutates in the loop. The
// no-org-in-any-schema invariant is checked statically across the real tool set.
// =============================================================================

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/google/uuid"

	"github.com/operationfb/accounting-saas/internal/kernel"
)

// ----- fakes -------------------------------------------------------------------

// scriptedModel returns a fixed sequence of pre-baked responses (one per model
// call). Responses are built from wire JSON so the SDK types are populated
// exactly as a real API response would be.
type scriptedModel struct {
	t       *testing.T
	replies []string
	calls   int
}

func (m *scriptedModel) CreateMessage(_ context.Context, _ anthropic.MessageNewParams) (*anthropic.Message, error) {
	if m.calls >= len(m.replies) {
		m.t.Fatalf("model called %d times but only %d replies were scripted", m.calls+1, len(m.replies))
	}
	body := m.replies[m.calls]
	m.calls++
	return unmarshalMessage(m.t, body)
}

// loopingModel always asks for the same tool — used to prove the iteration cap.
type loopingModel struct {
	t     *testing.T
	body  string
	calls int
}

func (m *loopingModel) CreateMessage(_ context.Context, _ anthropic.MessageNewParams) (*anthropic.Message, error) {
	m.calls++
	return unmarshalMessage(m.t, m.body)
}

func unmarshalMessage(t *testing.T, body string) (*anthropic.Message, error) {
	t.Helper()
	var msg anthropic.Message
	if err := json.Unmarshal([]byte(body), &msg); err != nil {
		t.Fatalf("bad scripted message JSON: %v", err)
	}
	return &msg, nil
}

// toolUseReply / textReply build the two message shapes the model produces.
func toolUseReply(id, name, inputJSON string) string {
	return `{"id":"msg","type":"message","role":"assistant","model":"claude-opus-4-8","stop_reason":"tool_use","content":[{"type":"tool_use","id":"` + id + `","name":"` + name + `","input":` + inputJSON + `}]}`
}

func textReply(text string) string {
	b, _ := json.Marshal(text)
	return `{"id":"msg","type":"message","role":"assistant","model":"claude-opus-4-8","stop_reason":"end_turn","content":[{"type":"text","text":` + string(b) + `}]}`
}

// recordedCall captures what a tool executor received.
type recordedCall struct {
	userID uuid.UUID
	orgID  uuid.UUID
	input  json.RawMessage
}

func recordingTool(name string, out toolResult, err error, sink *[]recordedCall) Tool {
	return Tool{
		Name: name,
		Exec: func(_ context.Context, u, o uuid.UUID, in json.RawMessage) (toolResult, error) {
			*sink = append(*sink, recordedCall{userID: u, orgID: o, input: in})
			return out, err
		},
	}
}

func newTestService(model modelClient, tools ...Tool) *Service {
	byName := make(map[string]Tool, len(tools))
	for _, t := range tools {
		byName[t.Name] = t
	}
	return &Service{model: model, toolByName: byName}
}

func userMsg(text string) []ChatMessage { return []ChatMessage{{Role: "user", Content: text}} }

// ----- tests -------------------------------------------------------------------

// The happy path: the model calls a tool, we dispatch it with the caller's
// identity, feed the result back, and return the model's final text.
func TestRunTurnDispatchesToolAndReturnsReply(t *testing.T) {
	wantUser := uuid.New()
	wantOrg := uuid.New()
	var calls []recordedCall

	model := &scriptedModel{t: t, replies: []string{
		toolUseReply("toolu_1", "list_expenses", "{}"),
		textReply("You have 2 expenses."),
	}}
	svc := newTestService(model, recordingTool("list_expenses", toolResult{Content: "[]"}, nil, &calls))

	reply, proposals, toolsUsed, err := svc.RunTurn(context.Background(), wantUser, wantOrg, userMsg("how many expenses?"))
	if err != nil {
		t.Fatalf("RunTurn: %v", err)
	}
	if reply != "You have 2 expenses." {
		t.Errorf("reply = %q", reply)
	}
	if len(proposals) != 0 {
		t.Errorf("expected no proposals, got %d", len(proposals))
	}
	if len(toolsUsed) != 1 || toolsUsed[0] != "list_expenses" {
		t.Errorf("toolsUsed = %v", toolsUsed)
	}
	if model.calls != 2 {
		t.Errorf("model calls = %d, want 2", model.calls)
	}
	// The security-critical assertion: the tool got the TOKEN's identity, not
	// anything the model supplied.
	if len(calls) != 1 {
		t.Fatalf("tool calls = %d, want 1", len(calls))
	}
	if calls[0].userID != wantUser || calls[0].orgID != wantOrg {
		t.Errorf("tool received user=%s org=%s, want user=%s org=%s", calls[0].userID, calls[0].orgID, wantUser, wantOrg)
	}
}

// A tool error is fed back as an error result (not fatal); the loop continues and
// the model can still produce a final answer.
func TestRunTurnRecoversFromToolError(t *testing.T) {
	var calls []recordedCall
	model := &scriptedModel{t: t, replies: []string{
		toolUseReply("toolu_1", "list_expenses", "{}"),
		textReply("Sorry, I couldn't access that."),
	}}
	svc := newTestService(model,
		recordingTool("list_expenses", toolResult{}, kernel.ErrForbidden("nope"), &calls))

	reply, _, _, err := svc.RunTurn(context.Background(), uuid.New(), uuid.New(), userMsg("show me all expenses"))
	if err != nil {
		t.Fatalf("RunTurn should recover from a tool error, got: %v", err)
	}
	if !strings.Contains(reply, "couldn't access") {
		t.Errorf("reply = %q", reply)
	}
	if model.calls != 2 {
		t.Errorf("model calls = %d, want 2 (error fed back, loop continued)", model.calls)
	}
}

// An unknown tool name does not crash the loop — it's reported back as an error
// result and the turn completes.
func TestRunTurnHandlesUnknownTool(t *testing.T) {
	model := &scriptedModel{t: t, replies: []string{
		toolUseReply("toolu_1", "does_not_exist", "{}"),
		textReply("done"),
	}}
	svc := newTestService(model) // no tools registered

	reply, _, _, err := svc.RunTurn(context.Background(), uuid.New(), uuid.New(), userMsg("hi"))
	if err != nil {
		t.Fatalf("RunTurn: %v", err)
	}
	if reply != "done" {
		t.Errorf("reply = %q", reply)
	}
}

// A propose_ tool returns a proposal for the user to confirm and does NOT mutate.
// We use the REAL propose_create_expense tool (it touches no database).
func TestRunTurnCollectsProposalWithoutMutating(t *testing.T) {
	var createTool Tool
	for _, tl := range proposeTools() {
		if tl.Name == "propose_create_expense" {
			createTool = tl
		}
	}
	if createTool.Name == "" {
		t.Fatal("propose_create_expense not found")
	}

	model := &scriptedModel{t: t, replies: []string{
		toolUseReply("toolu_1", "propose_create_expense", `{"description":"Team lunch","gross_value":"12.00"}`),
		textReply("I've prepared that expense — please review the category and click Confirm."),
	}}
	svc := newTestService(model, createTool)

	_, proposals, _, err := svc.RunTurn(context.Background(), uuid.New(), uuid.New(), userMsg("add a £12 lunch expense"))
	if err != nil {
		t.Fatalf("RunTurn: %v", err)
	}
	if len(proposals) != 1 {
		t.Fatalf("expected 1 proposal, got %d", len(proposals))
	}
	p := proposals[0]
	if p.Kind != "create_expense" {
		t.Errorf("kind = %q", p.Kind)
	}
	var payload map[string]any
	if err := json.Unmarshal(p.Payload, &payload); err != nil {
		t.Fatalf("payload: %v", err)
	}
	if payload["description"] != "Team lunch" || payload["gross_value"] != "12.00" {
		t.Errorf("payload = %v", payload)
	}
	if payload["currency"] != "GBP" {
		t.Errorf("currency should default to GBP, got %v", payload["currency"])
	}
	if payload["dated_on"] == "" {
		t.Errorf("dated_on should default to today")
	}
}

// A model that never stops calling tools must be cut off by the iteration cap and
// surface an error rather than loop forever.
func TestRunTurnEnforcesIterationCap(t *testing.T) {
	var calls []recordedCall
	model := &loopingModel{t: t, body: toolUseReply("toolu_x", "list_expenses", "{}")}
	svc := newTestService(model, recordingTool("list_expenses", toolResult{Content: "[]"}, nil, &calls))

	_, _, _, err := svc.RunTurn(context.Background(), uuid.New(), uuid.New(), userMsg("loop"))
	if err == nil {
		t.Fatal("expected an error when the iteration cap is hit")
	}
	if model.calls != maxIterations {
		t.Errorf("model calls = %d, want %d (the cap)", model.calls, maxIterations)
	}
}

// History validation: empty history, a non-user final turn, and unknown roles are
// all rejected before any model call.
func TestToMessagesValidation(t *testing.T) {
	if _, err := toMessages(nil); err == nil {
		t.Error("empty history should error")
	}
	if _, err := toMessages([]ChatMessage{{Role: "user", Content: "hi"}, {Role: "assistant", Content: "hello"}}); err == nil {
		t.Error("history ending on an assistant turn should error")
	}
	if _, err := toMessages([]ChatMessage{{Role: "system", Content: "x"}}); err == nil {
		t.Error("unknown role should error")
	}
	msgs, err := toMessages([]ChatMessage{{Role: "user", Content: "a"}, {Role: "assistant", Content: "b"}, {Role: "user", Content: "c"}})
	if err != nil {
		t.Fatalf("valid history: %v", err)
	}
	if len(msgs) != 3 {
		t.Errorf("messages = %d, want 3", len(msgs))
	}
}

// The multi-tenant invariant, checked statically across the ENTIRE tool set: no
// tool schema may expose an organisation/user id — those come only from the token.
// readTools captures the service pointers in closures but never dereferences them
// at construction, so nil services are safe here.
func TestNoToolExposesOrgOrUserInSchema(t *testing.T) {
	all := append(readTools(nil, nil, nil, nil, nil, nil, nil), proposeTools()...)
	forbidden := map[string]bool{
		"organisation_id": true, "organization_id": true, "org_id": true,
		"user_id": true, "userid": true, "user": true,
	}
	for _, tl := range all {
		for key := range tl.Properties {
			if forbidden[strings.ToLower(key)] {
				t.Errorf("tool %q exposes forbidden schema field %q", tl.Name, key)
			}
		}
	}
	if len(all) == 0 {
		t.Fatal("expected a non-empty tool set")
	}
}
