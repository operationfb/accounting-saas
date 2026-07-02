package tala

// service.go
// =============================================================================
// Service — the Tala agent. RunTurn runs one user turn to completion: it feeds
// the conversation to the model and services any tool calls (org-scoped to the
// caller) until the model returns a final text answer or the iteration cap is
// hit. The loop is deliberately hand-written (not the SDK's tool runner) so every
// tool call is org-scoped, audit-logged, and gate-able, and the model sits behind
// the modelClient seam for testing.
// =============================================================================

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/google/uuid"

	"github.com/operationfb/accounting-saas/internal/banking"
	"github.com/operationfb/accounting-saas/internal/bills"
	"github.com/operationfb/accounting-saas/internal/expenses"
	"github.com/operationfb/accounting-saas/internal/invoices"
	"github.com/operationfb/accounting-saas/internal/kernel"
	"github.com/operationfb/accounting-saas/internal/overview"
	"github.com/operationfb/accounting-saas/internal/reports"
	"github.com/operationfb/accounting-saas/internal/vat"
)

const (
	// maxIterations caps the model→tools→model loop so a misbehaving turn can't
	// spin forever (each iteration is one model call).
	maxIterations = 8
	// maxTokens bounds a single model reply. A chat answer is small; keeping this
	// modest also keeps non-streaming latency in check.
	maxTokens = 4096
)

// Service holds the model client and the (stateless) tool registry, both built
// once at startup. Per-request state (org, user, history) is passed into RunTurn.
type Service struct {
	model      modelClient
	toolByName map[string]Tool
	toolParams []anthropic.ToolUnionParam
}

// NewService wires the model client and builds the tool set over the existing
// domain services. Called once from main (only when ANTHROPIC_API_KEY is set).
func NewService(
	model modelClient,
	exp *expenses.Service,
	inv *invoices.Service,
	bill *bills.Service,
	bank *banking.Service,
	vatSvc *vat.Service,
	rep *reports.Service,
	ov *overview.Service,
) *Service {
	tools := append(readTools(exp, inv, bill, bank, vatSvc, rep, ov), proposeTools()...)

	byName := make(map[string]Tool, len(tools))
	params := make([]anthropic.ToolUnionParam, 0, len(tools))
	for _, t := range tools {
		byName[t.Name] = t
		params = append(params, t.toParam())
	}
	return &Service{model: model, toolByName: byName, toolParams: params}
}

// RunTurn answers one user turn. It returns the reply text, any guarded-write
// proposals collected during the turn, and the names of the tools invoked (for
// UI transparency).
func (s *Service) RunTurn(ctx context.Context, userID, orgID uuid.UUID, history []ChatMessage) (string, []ProposedAction, []string, error) {
	msgs, err := toMessages(history)
	if err != nil {
		return "", nil, nil, err
	}

	var proposals []ProposedAction
	var toolsUsed []string

	for i := 0; i < maxIterations; i++ {
		resp, err := s.model.CreateMessage(ctx, anthropic.MessageNewParams{
			MaxTokens: maxTokens,
			// The system prompt is a stable, frozen prefix — cache it so repeated
			// turns pay for it once (prompt caching is a prefix match).
			System: []anthropic.TextBlockParam{{
				Text:         systemPrompt,
				CacheControl: anthropic.NewCacheControlEphemeralParam(),
			}},
			Thinking: anthropic.ThinkingConfigParamUnion{OfAdaptive: &anthropic.ThinkingConfigAdaptiveParam{}},
			Tools:    s.toolParams,
			Messages: msgs,
		})
		if err != nil {
			return "", nil, nil, kernel.ErrInternal(fmt.Errorf("tala model call: %w", err))
		}

		// Keep the assistant turn (including any thinking / tool_use blocks) in the
		// history — the API requires them echoed back on the next iteration.
		msgs = append(msgs, resp.ToParam())

		if resp.StopReason != anthropic.StopReasonToolUse {
			return collectText(resp), proposals, toolsUsed, nil
		}

		// Service every tool_use block; all results go back in one user turn.
		var results []anthropic.ContentBlockParamUnion
		for _, block := range resp.Content {
			tu, ok := block.AsAny().(anthropic.ToolUseBlock)
			if !ok {
				continue
			}
			toolsUsed = append(toolsUsed, tu.Name)

			tool, found := s.toolByName[tu.Name]
			if !found {
				results = append(results, anthropic.NewToolResultBlock(tu.ID, "unknown tool: "+tu.Name, true))
				continue
			}

			out, execErr := tool.Exec(ctx, userID, orgID, tu.Input)
			if execErr != nil {
				// Feed the SAFE client message back to the model as an error result
				// so it can recover; log the real cause for genuine internal errors.
				appErr := kernel.AsAppError(execErr)
				if appErr.Code == kernel.ErrCodeInternal {
					slog.Error("tala tool failed", "tool", tu.Name, "org", orgID, "err", appErr.Err)
				}
				results = append(results, anthropic.NewToolResultBlock(tu.ID, appErr.Message, true))
				continue
			}

			slog.Info("tala tool call", "tool", tu.Name, "org", orgID)
			if out.Proposal != nil {
				proposals = append(proposals, *out.Proposal)
			}
			results = append(results, anthropic.NewToolResultBlock(tu.ID, out.Content, false))
		}
		msgs = append(msgs, anthropic.NewUserMessage(results...))
	}

	return "", proposals, toolsUsed, kernel.ErrInternal(fmt.Errorf("tala reached the %d-step limit without a final answer", maxIterations))
}

// toMessages converts the SPA's chat history into SDK message params. The final
// message must be from the user (that's the turn Tala answers).
func toMessages(history []ChatMessage) ([]anthropic.MessageParam, error) {
	if len(history) == 0 {
		return nil, kernel.ErrValidation("no messages provided", nil)
	}
	out := make([]anthropic.MessageParam, 0, len(history))
	for _, m := range history {
		content := strings.TrimSpace(m.Content)
		if content == "" {
			return nil, kernel.ErrValidation("a message has empty content", nil)
		}
		switch m.Role {
		case "user":
			out = append(out, anthropic.NewUserMessage(anthropic.NewTextBlock(content)))
		case "assistant":
			out = append(out, anthropic.NewAssistantMessage(anthropic.NewTextBlock(content)))
		default:
			return nil, kernel.ErrValidation(fmt.Sprintf("unknown message role %q", m.Role), nil)
		}
	}
	if history[len(history)-1].Role != "user" {
		return nil, kernel.ErrValidation("the last message must be from the user", nil)
	}
	return out, nil
}

// collectText concatenates the text blocks of a final assistant message.
func collectText(resp *anthropic.Message) string {
	var b strings.Builder
	for _, block := range resp.Content {
		if tb, ok := block.AsAny().(anthropic.TextBlock); ok {
			b.WriteString(tb.Text)
		}
	}
	return strings.TrimSpace(b.String())
}
