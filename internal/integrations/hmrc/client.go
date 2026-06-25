package hmrc

// client.go
// =============================================================================
// HMRC's implementation of integrations.OAuthClient — auth-only (OAuth 2.0
// Authorization Code Grant). It builds the authorize URL and exchanges/refreshes
// tokens for HMRC's Making Tax Digital APIs.
//
// Key difference from the FreeAgent client: HMRC passes credentials in the
// form BODY (client_id, client_secret as POST params), not HTTP Basic auth.
//
// HMRC OAuth facts baked in here:
//   - authorize endpoint:  {host}/oauth/authorize
//   - token endpoint:      {host}/oauth/token  (form body, not Basic auth)
//   - scope required:      "read:vat write:vat" (added to authorize URL)
//   - access token:        ~4 hours (expires_in: 14400)
//   - refresh token:       ~18 months
//   - sandbox host:        https://test-api.service.hmrc.gov.uk
//   - production host:     https://api.service.hmrc.gov.uk
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
// Must match the row already in provider_credentials: provider = 'hmrc'.
const ProviderKey = "hmrc"

const (
	prodHost    = "https://api.service.hmrc.gov.uk"
	sandboxHost = "https://test-api.service.hmrc.gov.uk"

	authorizePath = "/oauth/authorize"
	tokenPath     = "/oauth/token"

	// scope is the fixed VAT MTD scope — both read (obligations, returns) and
	// write (submit return) are requested together at connect time.
	scope = "read:vat write:vat"
)

// Client talks to one HMRC environment (prod or sandbox), fixed at construction.
// It holds no credentials — those are passed into each call from provider_credentials.
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
// (an httptest.Server standing in for HMRC).
func NewClientWithHost(host string) *Client {
	return &Client{host: host, httpClient: &http.Client{Timeout: 20 * time.Second}}
}

// APIBaseURL is the VAT MTD API root. The VAT service constructs full endpoint
// URLs as {APIBaseURL}/{vrn}/obligations etc.
func (c *Client) APIBaseURL() string { return c.host + "/organisations/vat" }

// AuthorizeURL builds the HMRC consent URL the admin's browser is sent to.
// HMRC requires the scope parameter in the authorize URL.
func (c *Client) AuthorizeURL(clientID, redirectURI, state string) string {
	q := url.Values{}
	q.Set("client_id", clientID)
	q.Set("response_type", "code")
	q.Set("redirect_uri", redirectURI)
	q.Set("state", state)
	q.Set("scope", scope)
	return c.host + authorizePath + "?" + q.Encode()
}

// ExchangeCode swaps the authorisation code (from the callback) for tokens.
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

// postToken POSTs to HMRC's token endpoint with credentials in the form BODY
// (HMRC does NOT use HTTP Basic auth — the client_id and client_secret go as
// regular POST form fields alongside the grant params).
func (c *Client) postToken(ctx context.Context, clientID, clientSecret string, form url.Values) (integrations.TokenResponse, error) {
	var out integrations.TokenResponse

	// Credentials go in the body, not the Authorization header.
	form.Set("client_id", clientID)
	form.Set("client_secret", clientSecret)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.host+tokenPath, strings.NewReader(form.Encode()))
	if err != nil {
		return out, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return out, fmt.Errorf("hmrc token request failed: %w", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return out, fmt.Errorf("hmrc token endpoint returned %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return out, fmt.Errorf("decode hmrc token response: %w", err)
	}
	if out.AccessToken == "" {
		return out, fmt.Errorf("hmrc token response missing access_token")
	}
	return out, nil
}

// Compile-time assertion that Client satisfies the shared interface.
var _ integrations.OAuthClient = (*Client)(nil)
