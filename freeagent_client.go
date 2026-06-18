package main

// freeagent_client.go
// =============================================================================
// freeAgentClient — a thin HTTP client for FreeAgent's OAuth endpoints.
//
// This is the ONLY part of the monolith that talks to FreeAgent directly, and it
// is deliberately *auth-only*: it builds the authorize URL and exchanges/refreshes
// OAuth tokens. It knows NOTHING about expenses or field mapping — translating an
// expense into a FreeAgent payload and calling POST /v2/expenses is the external
// Cloud Workflow's job. Keeping this client tiny is the point.
//
// It is a plain concrete struct (like gotenbergRenderer / the SMTP sender), not
// behind an interface: tests exercise it against an httptest.Server.
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
)

const (
	// FreeAgent hosts. Only the host differs between production and sandbox; the
	// OAuth/API paths below are identical. These are static, well-known URLs —
	// they do not rotate, so they live as constants (not config).
	freeAgentProdHost    = "https://api.freeagent.com"
	freeAgentSandboxHost = "https://api.sandbox.freeagent.com"

	// OAuth paths (appended to the chosen host).
	freeAgentApprovePath = "/v2/approve_app"     // where we send the admin's browser to consent
	freeAgentTokenPath   = "/v2/token_endpoint"  // code->token exchange + refresh
)

// freeAgentClient talks to one FreeAgent environment (prod or sandbox), fixed at
// construction by the FREEAGENT_SANDBOX switch.
type freeAgentClient struct {
	host       string // scheme+host, e.g. "https://api.freeagent.com"
	httpClient *http.Client
}

// newFreeAgentClient builds a client for prod (sandbox=false) or sandbox
// (sandbox=true). It holds no credentials — those are per-org and passed into
// each call — so it is always safe to construct at startup.
func newFreeAgentClient(sandbox bool) *freeAgentClient {
	host := freeAgentProdHost
	if sandbox {
		host = freeAgentSandboxHost
	}
	return &freeAgentClient{
		host:       host,
		httpClient: &http.Client{Timeout: 20 * time.Second},
	}
}

// apiBaseURL is the REST API root the Cloud Workflow targets (e.g. it appends
// /v2/expenses and /v2/users). The internal token endpoint hands this back to the
// workflow at runtime so the host that minted the token and the host the workflow
// calls can never drift apart.
func (c *freeAgentClient) apiBaseURL() string { return c.host + "/v2" }

// authorizeURL builds the URL we redirect the admin's browser to so they can
// approve our app for their FreeAgent company. state is an opaque, signed token
// we mint (a short-lived PASETO) so the callback can recover the org and reject
// forged/stale requests (CSRF).
func (c *freeAgentClient) authorizeURL(clientID, redirectURI, state string) string {
	q := url.Values{}
	q.Set("client_id", clientID)
	q.Set("response_type", "code")
	q.Set("redirect_uri", redirectURI)
	q.Set("state", state)
	return c.host + freeAgentApprovePath + "?" + q.Encode()
}

// oauthTokenResponse is the JSON FreeAgent returns from the token endpoint, for
// both the initial code exchange and a refresh.
type oauthTokenResponse struct {
	AccessToken           string `json:"access_token"`
	RefreshToken          string `json:"refresh_token"`
	TokenType             string `json:"token_type"`
	ExpiresIn             int    `json:"expires_in"`               // access-token life in seconds (~3600)
	RefreshTokenExpiresIn int    `json:"refresh_token_expires_in"` // refresh-token life in seconds (~20y)
}

// ExchangeCode swaps the authorization code (from the callback) for tokens. This
// runs once per org, at the end of the interactive connect.
func (c *freeAgentClient) ExchangeCode(ctx context.Context, clientID, clientSecret, code, redirectURI string) (oauthTokenResponse, error) {
	form := url.Values{}
	form.Set("grant_type", "authorization_code")
	form.Set("code", code)
	form.Set("redirect_uri", redirectURI)
	return c.postToken(ctx, clientID, clientSecret, form)
}

// RefreshToken mints a fresh access token from the stored refresh token. This runs
// server-side, with no user present, whenever the access token is near expiry.
func (c *freeAgentClient) RefreshToken(ctx context.Context, clientID, clientSecret, refreshToken string) (oauthTokenResponse, error) {
	form := url.Values{}
	form.Set("grant_type", "refresh_token")
	form.Set("refresh_token", refreshToken)
	return c.postToken(ctx, clientID, clientSecret, form)
}

// postToken POSTs a form to the token endpoint with HTTP Basic auth (client id as
// username, client secret as password) and decodes the JSON response. Errors are
// plain errors — the service layer wraps them into AppError. We never echo the
// request body (it carries the secret + code) in an error.
func (c *freeAgentClient) postToken(ctx context.Context, clientID, clientSecret string, form url.Values) (oauthTokenResponse, error) {
	var out oauthTokenResponse

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.host+freeAgentTokenPath, strings.NewReader(form.Encode()))
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
		// Surface the status + trimmed body for diagnosis; the grant params (with
		// the secret) are deliberately NOT included.
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
