package freeagent

// client.go
// =============================================================================
// FreeAgent's implementation of integrations.OAuthClient — the ONLY FreeAgent-
// specific code in the integration. It is deliberately auth-only: it builds the
// authorize URL and exchanges/refreshes OAuth tokens. It knows NOTHING about
// expenses or field mapping — translating an expense into a FreeAgent payload and
// calling POST /v2/expenses is the external Cloud Workflow's job.
//
// FreeAgent OAuth facts baked in here:
//   - authorize endpoint:  {host}/v2/approve_app   (the one interactive step)
//   - token endpoint:       {host}/v2/token_endpoint (HTTP Basic auth: id:secret)
//   - access token lives ~1h; refresh token ~20y (we store + refresh server-side)
//   - sandbox swaps only the host; the paths are static constants
// =============================================================================

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/operationfb/accounting-saas/internal/integrations"
)

// ProviderKey is the provider identifier — used as BOTH the DB key
// (provider_credentials / organisation_integrations) and the public URL slug.
const ProviderKey = "freeagent"

const (
	// FreeAgent hosts. Only the host differs between prod and sandbox; the
	// OAuth paths below are identical static constants (they do not rotate).
	prodHost    = "https://api.freeagent.com"
	sandboxHost = "https://api.sandbox.freeagent.com"

	// ApprovePath / TokenPath are the OAuth paths appended to the chosen host.
	ApprovePath = "/v2/approve_app"    // browser consent
	TokenPath   = "/v2/token_endpoint" // code->token exchange + refresh
)

// MaxAttachmentBytes is FreeAgent's documented cap for an expense attachment: 5MB
// (https://dev.freeagent.com/docs/expenses). We take the conservative DECIMAL
// reading (5,000,000, not 5 MiB) so a borderline receipt is skipped on our side
// rather than rejected by FreeAgent mid-push. main passes this into
// integrations.NewService as a plain int64 — the integrations package never
// imports this one (the provider-agnostic guardrail).
const MaxAttachmentBytes int64 = 5_000_000

// Client talks to one FreeAgent environment (prod or sandbox), fixed at
// construction. It holds no credentials — those are passed into each call.
type Client struct {
	host       string
	httpClient *http.Client
}

// NewClient builds a Client for prod (sandbox=false) or sandbox (sandbox=true).
func NewClient(sandbox bool) *Client {
	host := prodHost
	if sandbox {
		host = sandboxHost
	}
	return &Client{host: host, httpClient: &http.Client{Timeout: 20 * time.Second}}
}

// NewClientWithHost builds a Client pointed at an explicit host — used by tests
// (an httptest.Server standing in for FreeAgent) and any non-standard deployment.
func NewClientWithHost(host string) *Client {
	return &Client{host: host, httpClient: &http.Client{Timeout: 20 * time.Second}}
}

// APIBaseURL is the REST API root the Cloud Workflow targets.
func (c *Client) APIBaseURL() string { return c.host + "/v2" }

// AuthorizeURL builds the URL the admin's browser is redirected to so they can
// approve our app for their FreeAgent company.
func (c *Client) AuthorizeURL(clientID, redirectURI, state string) string {
	q := url.Values{}
	q.Set("client_id", clientID)
	q.Set("response_type", "code")
	q.Set("redirect_uri", redirectURI)
	q.Set("state", state)
	return c.host + ApprovePath + "?" + q.Encode()
}

// ExchangeCode swaps the authorization code (from the callback) for tokens.
func (c *Client) ExchangeCode(ctx context.Context, clientID, clientSecret, code, redirectURI string) (integrations.TokenResponse, error) {
	form := url.Values{}
	form.Set("grant_type", "authorization_code")
	form.Set("code", code)
	form.Set("redirect_uri", redirectURI)
	return c.postToken(ctx, clientID, clientSecret, form)
}

// RefreshToken mints a fresh access token from the stored refresh token.
func (c *Client) RefreshToken(ctx context.Context, clientID, clientSecret, refreshToken string) (integrations.TokenResponse, error) {
	form := url.Values{}
	form.Set("grant_type", "refresh_token")
	form.Set("refresh_token", refreshToken)
	return c.postToken(ctx, clientID, clientSecret, form)
}

// postToken POSTs a form to the token endpoint with HTTP Basic auth (client id as
// username, client secret as password) and decodes the JSON response. We never
// echo the request body (it carries the secret + code) in an error.
func (c *Client) postToken(ctx context.Context, clientID, clientSecret string, form url.Values) (integrations.TokenResponse, error) {
	var out integrations.TokenResponse

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.host+TokenPath, strings.NewReader(form.Encode()))
	if err != nil {
		return out, err
	}
	req.SetBasicAuth(clientID, clientSecret)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return out, fmt.Errorf("freeagent token request failed: %w", err)
	}
	defer resp.Body.Close()

	// Cap the read so a misbehaving endpoint can't exhaust memory.
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		// Surface the status + trimmed body; the grant params (with the secret)
		// are deliberately NOT included.
		return out, fmt.Errorf("freeagent token endpoint returned %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return out, fmt.Errorf("decode freeagent token response: %w", err)
	}
	if out.AccessToken == "" {
		return out, fmt.Errorf("freeagent token response missing access_token")
	}
	return out, nil
}

// Compile-time assertion that Client satisfies the shared interface.
var _ integrations.OAuthClient = (*Client)(nil)
