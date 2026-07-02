package tala

// client.go
// =============================================================================
// modelClient — the seam over the Anthropic Messages API.
//
// RunTurn (service.go) builds the full request (system prompt, tools, adaptive
// thinking, the running message history); the implementation only injects the
// configured model id and forwards to the SDK. This narrow interface is the ONE
// external dependency we mock in tests: a fake returns scripted responses so the
// whole agent loop is exercised without a network call (everything else — the
// tools — runs against real Postgres, per the project's testing rule).
// =============================================================================

import (
	"context"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
)

// modelClient is the minimal surface RunTurn needs from the LLM.
type modelClient interface {
	CreateMessage(ctx context.Context, params anthropic.MessageNewParams) (*anthropic.Message, error)
}

// anthropicClient is the production modelClient, wrapping the official SDK.
type anthropicClient struct {
	client anthropic.Client
	model  anthropic.Model
}

// NewAnthropicClient builds the production model client. apiKey is ANTHROPIC_API_KEY;
// model is the model id (e.g. "claude-opus-4-8").
func NewAnthropicClient(apiKey, model string) *anthropicClient {
	return &anthropicClient{
		client: anthropic.NewClient(option.WithAPIKey(apiKey)),
		model:  anthropic.Model(model),
	}
}

// CreateMessage injects the configured model and calls the Messages API.
func (c *anthropicClient) CreateMessage(ctx context.Context, params anthropic.MessageNewParams) (*anthropic.Message, error) {
	params.Model = c.model
	return c.client.Messages.New(ctx, params)
}
