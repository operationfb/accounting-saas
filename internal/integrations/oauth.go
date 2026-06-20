package integrations

// oauth.go
// =============================================================================
// The seam between the generic integration machinery and a specific provider.
//
// Everything in this package is provider-agnostic; the ONE thing that differs per
// provider is the OAuth surface (hosts, URL shapes, token decoding). That is
// captured by OAuthClient, which each provider package implements
// (e.g. internal/integrations/freeagent). The Service depends only on this
// interface, so a future Xero/QuickBooks is just another OAuthClient + a Service
// instance — no change here.
// =============================================================================

import "context"

// TokenResponse is the standard OAuth2 token-endpoint response, shared across
// providers. The field set is what providers return for both the initial code
// exchange and a refresh.
type TokenResponse struct {
	AccessToken           string `json:"access_token"`
	RefreshToken          string `json:"refresh_token"`
	TokenType             string `json:"token_type"`
	ExpiresIn             int    `json:"expires_in"`               // access-token life (seconds)
	RefreshTokenExpiresIn int    `json:"refresh_token_expires_in"` // refresh-token life (seconds)
}

// OAuthClient is the provider-specific OAuth surface the Service depends on. It is
// deliberately auth-only — it knows nothing about expenses or field mapping (that
// is the external workflow's job).
type OAuthClient interface {
	// AuthorizeURL builds the consent URL the admin's browser is sent to.
	AuthorizeURL(clientID, redirectURI, state string) string
	// ExchangeCode swaps an authorization code for tokens (end of the connect).
	ExchangeCode(ctx context.Context, clientID, clientSecret, code, redirectURI string) (TokenResponse, error)
	// RefreshToken mints a fresh access token from a refresh token (server-side).
	RefreshToken(ctx context.Context, clientID, clientSecret, refreshToken string) (TokenResponse, error)
	// APIBaseURL is the REST API root the workflow targets. It is handed to the
	// workflow at runtime so the host it calls can never drift from the host the
	// token was minted against.
	APIBaseURL() string
}
