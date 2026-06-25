package integrations

// service.go
// =============================================================================
// Service — the generic, provider-agnostic half of a third-party push
// integration: the OAuth connect lifecycle and credential/token custody. It does
// NOT push expenses or map fields (that's the external Cloud Workflow's job).
//
// Provider-specific behaviour is injected as an OAuthClient (oauth.go). The
// provider key is instance config — it is BOTH the DB key (provider_credentials /
// organisation_integrations) and the public URL slug — so a future Xero is just
// another Service instance.
//
// Access: every user-facing operation is owner/admin only; the org comes from the
// caller's token. The public OAuth callback is the exception — it carries no token
// and instead trusts the signed `state` issued at connect.
// =============================================================================

import (
	"context"
	"errors"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	auth "github.com/operationfb/accounting-saas/db/auth"
	integrationsdb "github.com/operationfb/accounting-saas/db/integrations"
	"github.com/operationfb/accounting-saas/internal/kernel"
	"github.com/operationfb/accounting-saas/token"
)

// connectStateTTL bounds how long a "connect" link stays valid. The state is a
// signed PASETO carrying the org, so a short life limits the CSRF window.
const connectStateTTL = 15 * time.Minute

// Service holds the integration + auth query sets, the provider's OAuth client,
// the provider key, and the two base URLs the OAuth flow needs.
type Service struct {
	iq          integrationsdb.Querier
	authQueries auth.Querier
	oauth       OAuthClient
	tokenMaker  token.Maker

	// attachments fetches an expense's primary receipt for the push (a narrow
	// cross-domain seam, like the Handler's ExpenseRepublisher). nil disables
	// attachment push — AttachmentForPush then behaves as "no attachment".
	attachments AttachmentFetcher
	// maxAttachmentBytes is the provider's attachment size cap, passed through to
	// the fetcher's size guard. Injected by main from the provider's own constant
	// (e.g. freeagent.MaxAttachmentBytes), so this package stays provider-agnostic.
	maxAttachmentBytes int64

	// provider is the key this instance manages ("freeagent", …) — BOTH the DB key
	// and the public URL slug (/api/v1/{provider}/callback, ?{provider}=connected).
	provider string

	// apiPublicURL is OUR backend's externally reachable base URL — it builds the
	// OAuth redirect_uri (the BACKEND, not the frontend appBaseURL).
	apiPublicURL string
	// appBaseURL is the frontend SPA base — after the callback we send the browser
	// back here to the settings screen.
	appBaseURL string
}

// NewService is the constructor, called once per provider in main.go. attachments
// + maxAttachmentBytes wire the receipt-push fetch (the fetcher lives in the
// expenses/attachments domain; the size cap is the provider's own constant).
func NewService(iq integrationsdb.Querier, authQueries auth.Querier, oauth OAuthClient, attachments AttachmentFetcher, maxAttachmentBytes int64, provider string, tokenMaker token.Maker, apiPublicURL, appBaseURL string) *Service {
	return &Service{
		iq:                 iq,
		authQueries:        authQueries,
		oauth:              oauth,
		attachments:        attachments,
		maxAttachmentBytes: maxAttachmentBytes,
		tokenMaker:         tokenMaker,
		provider:           provider,
		apiPublicURL:       apiPublicURL,
		appBaseURL:         appBaseURL,
	}
}

// Provider returns the provider key/slug — the Handler uses it to build routes.
func (s *Service) Provider() string { return s.provider }

// =============================================================================
// DTOs
// =============================================================================

// StatusResponse is what the settings screen renders. It NEVER includes any
// secret — only whether the GLOBAL app credentials are configured, whether THIS
// org is connected, and since when.
type StatusResponse struct {
	HasCredentials bool    `json:"has_credentials"`
	Connected      bool    `json:"connected"`
	ConnectedAt    *string `json:"connected_at,omitempty"`
}

// PushStatusResponse is the per-expense push outcome the SPA renders as a
// "Pushed ✓ / Failed ⚠" badge on an approved expense.
type PushStatusResponse struct {
	State       string  `json:"state"`                  // "pushed" | "failed" | "none"
	ExternalURL *string `json:"external_url,omitempty"` // external expense URL, on success
	Error       *string `json:"error,omitempty"`        // last push error message, on failure
	PushedAt    *string `json:"pushed_at,omitempty"`    // RFC3339 — when the push was attempted
	Connected   bool    `json:"connected"`              // org has a connected integration
}

// =============================================================================
// AUTHORIZATION
// =============================================================================

// requireAdmin confirms the caller is an active owner/admin of the org.
func (s *Service) requireAdmin(ctx context.Context, userID, orgID uuid.UUID) error {
	role, err := kernel.AuthorizeMember(ctx, s.authQueries, userID, orgID)
	if err != nil {
		return err
	}
	if !kernel.IsOrgAdmin(role) {
		return kernel.ErrForbidden("only owners and admins can manage integrations")
	}
	return nil
}

// =============================================================================
// STATUS
// =============================================================================

// GetStatus reports whether the GLOBAL app credentials are configured and whether
// THIS org is connected. Both "no credentials" and "never connected" are normal.
func (s *Service) GetStatus(ctx context.Context, authUserID, authOrgID uuid.UUID) (*StatusResponse, error) {
	if err := s.requireAdmin(ctx, authUserID, authOrgID); err != nil {
		return nil, err
	}

	resp := &StatusResponse{}

	// Global app credentials: configured iff the provider_credentials row exists.
	if _, err := s.iq.GetProviderCredentials(ctx, s.provider); err != nil {
		if !errors.Is(err, pgx.ErrNoRows) {
			return nil, kernel.ErrInternal(err)
		}
		// pgx.ErrNoRows → not configured; has_credentials stays false.
	} else {
		resp.HasCredentials = true
	}

	// Per-org connection: connected iff this org has a row with connected_at set.
	row, err := s.iq.GetIntegration(ctx, integrationsdb.GetIntegrationParams{
		OrganisationID: authOrgID,
		Provider:       s.provider,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return resp, nil // never connected
		}
		return nil, kernel.ErrInternal(err)
	}
	resp.Connected = row.ConnectedAt.Valid
	if row.ConnectedAt.Valid {
		ts := row.ConnectedAt.Time.Format(time.RFC3339)
		resp.ConnectedAt = &ts
	}
	return resp, nil
}

// Disconnect drops THIS org's tokens (so it can reconnect with one click). The
// GLOBAL app credentials are untouched. Idempotent.
func (s *Service) Disconnect(ctx context.Context, authUserID, authOrgID uuid.UUID) error {
	if err := s.requireAdmin(ctx, authUserID, authOrgID); err != nil {
		return err
	}
	if err := s.iq.ClearIntegrationTokens(ctx, integrationsdb.ClearIntegrationTokensParams{
		OrganisationID: authOrgID,
		Provider:       s.provider,
	}); err != nil {
		return kernel.ErrInternal(err)
	}
	return nil
}

// =============================================================================
// EXPENSE PUSH STATUS (the detail-page badge)
// =============================================================================

// GetExpensePushStatus reports whether — and how — an expense was pushed to the
// org's integration. Owner/admin only and org-scoped: the push row is reached
// through the org's own integration_id, so a cross-tenant expense id finds no row
// and reads back as "none". Reuses GetIntegration + GetExpensePushResult.
func (s *Service) GetExpensePushStatus(ctx context.Context, authUserID, authOrgID uuid.UUID, id string) (*PushStatusResponse, error) {
	if err := s.requireAdmin(ctx, authUserID, authOrgID); err != nil {
		return nil, err
	}
	expenseUUID, err := uuid.Parse(id)
	if err != nil {
		return nil, kernel.ErrValidation("id is not a valid UUID", err)
	}

	integ, err := s.iq.GetIntegration(ctx, integrationsdb.GetIntegrationParams{
		OrganisationID: authOrgID,
		Provider:       s.provider,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return &PushStatusResponse{State: "none"}, nil
		}
		return nil, kernel.ErrInternal(err)
	}

	resp := &PushStatusResponse{State: "none", Connected: integ.ConnectedAt.Valid}

	res, err := s.iq.GetExpensePushResult(ctx, integrationsdb.GetExpensePushResultParams{
		IntegrationID: integ.ID,
		ExpenseID:     expenseUUID,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return resp, nil // connected (maybe) but never attempted
		}
		return nil, kernel.ErrInternal(err)
	}

	switch {
	case res.ExternalExpenseRef.Valid && res.ExternalExpenseRef.String != "":
		resp.State = "pushed"
		ref := res.ExternalExpenseRef.String
		resp.ExternalURL = &ref
	case res.PushError.Valid && res.PushError.String != "":
		resp.State = "failed"
		msg := res.PushError.String
		resp.Error = &msg
	}
	if res.PushedAt.Valid {
		ts := res.PushedAt.Time.Format(time.RFC3339)
		resp.PushedAt = &ts
	}
	return resp, nil
}

// =============================================================================
// CROSS-DOMAIN SEAM (no admin gate — used by other service packages)
// =============================================================================

// IsConnected reports whether this org has an active connection for this
// provider. No admin gate — any service may check. Returns the connected-at
// time when true.
func (s *Service) IsConnected(ctx context.Context, orgID uuid.UUID) (bool, *time.Time) {
	row, err := s.iq.GetIntegration(ctx, integrationsdb.GetIntegrationParams{
		OrganisationID: orgID,
		Provider:       s.provider,
	})
	if err != nil || !row.ConnectedAt.Valid {
		return false, nil
	}
	t := row.ConnectedAt.Time
	return true, &t
}

// GetToken returns a valid access token and the provider's API base URL for
// the given org, refreshing the token server-side if it is near expiry. Returns
// a 409 AppError when the org is not connected. No admin gate — called by other
// service packages (e.g. the VAT service for HMRC submissions).
func (s *Service) GetToken(ctx context.Context, orgID uuid.UUID) (accessToken, apiBaseURL string, err error) {
	tok, err := s.TokenForOrg(ctx, orgID)
	if err != nil {
		return "", "", err
	}
	return tok.AccessToken, tok.APIBaseURL, nil
}

// =============================================================================
// OAUTH CONNECT FLOW
// =============================================================================

// BuildConnectURL returns the authorize URL the SPA should navigate to. It
// returns JSON (not a 302) because this endpoint is bearer-authed and a top-level
// browser redirect can't carry the SPA's Authorization header.
//
// The client_id comes from the GLOBAL provider_credentials row. A missing row
// means the integration isn't set up (an operator must add it in the DB) → 422.
func (s *Service) BuildConnectURL(ctx context.Context, authUserID, authOrgID uuid.UUID) (string, error) {
	if err := s.requireAdmin(ctx, authUserID, authOrgID); err != nil {
		return "", err
	}
	creds, err := s.iq.GetProviderCredentials(ctx, s.provider)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", kernel.ErrValidation("this integration is not configured — no app credentials are set", nil)
		}
		return "", kernel.ErrInternal(err)
	}

	// state = a short-lived signed token carrying the org (and the initiating
	// admin). The public callback verifies it to recover the org and reject
	// forged/stale requests. Reuses the PASETO maker — no new secret.
	state, err := s.tokenMaker.CreateToken(authUserID, authOrgID, connectStateTTL)
	if err != nil {
		return "", kernel.ErrInternal(err)
	}
	return s.oauth.AuthorizeURL(creds.ClientID, s.redirectURI(), state), nil
}

// HandleCallback completes the connect: verify the signed state → recover the org,
// exchange the code for tokens, store them. It always returns a redirect URL back
// to the SPA; the second return is a non-nil internal cause to LOG.
func (s *Service) HandleCallback(ctx context.Context, code, state string) (string, error) {
	payload, err := s.tokenMaker.VerifyToken(state)
	if err != nil {
		return s.callbackErrorURL("invalid_state"), nil
	}
	orgID := payload.OrganisationID

	if strings.TrimSpace(code) == "" {
		return s.callbackErrorURL("missing_code"), nil
	}

	creds, err := s.iq.GetProviderCredentials(ctx, s.provider)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return s.callbackErrorURL("not_configured"), nil
		}
		return s.callbackErrorURL("internal"), kernel.ErrInternal(err)
	}

	tok, err := s.oauth.ExchangeCode(ctx, creds.ClientID, creds.ClientSecret, code, s.redirectURI())
	if err != nil {
		return s.callbackErrorURL("exchange_failed"), kernel.ErrInternal(err)
	}

	// SetIntegrationTokens UPSERTs, so this CREATES the org's
	// organisation_integrations row on its first successful connect.
	if err := s.iq.SetIntegrationTokens(ctx, integrationsdb.SetIntegrationTokensParams{
		OrganisationID: orgID,
		Provider:       s.provider,
		AccessToken:    pgtype.Text{String: tok.AccessToken, Valid: true},
		RefreshToken:   pgtype.Text{String: tok.RefreshToken, Valid: tok.RefreshToken != ""},
		TokenExpiresAt: pgtype.Timestamptz{Time: time.Now().Add(time.Duration(tok.ExpiresIn) * time.Second), Valid: tok.ExpiresIn > 0},
	}); err != nil {
		return s.callbackErrorURL("internal"), kernel.ErrInternal(err)
	}

	return s.callbackSuccessURL(), nil
}

// =============================================================================
// URL HELPERS (provider-parameterised)
// =============================================================================

// redirectURI is the backend callback the provider sends the browser back to. It
// MUST match the redirect URI registered in the provider's app.
func (s *Service) redirectURI() string {
	return strings.TrimRight(s.apiPublicURL, "/") + "/api/v1/" + s.provider + "/callback"
}

func (s *Service) callbackSuccessURL() string {
	return strings.TrimRight(s.appBaseURL, "/") + "/settings/integrations?" + s.provider + "=connected"
}

func (s *Service) callbackErrorURL(reason string) string {
	return strings.TrimRight(s.appBaseURL, "/") + "/settings/integrations?" + s.provider + "=error&reason=" + url.QueryEscape(reason)
}
