package main

// integration_service.go
// =============================================================================
// IntegrationService — the monolith's half of the third-party push integration
// (FreeAgent first). Its job is the OAuth lifecycle and credential/token custody:
//
//   - save the org admin's OAuth app credentials (client_id / client_secret),
//   - run the one-time interactive connect (build authorize URL → handle callback
//     → store tokens),
//   - report connection status, and disconnect.
//
// It explicitly does NOT push expenses or map fields — that lives in the external
// Cloud Workflow. This service is a thin layer over the integrations queries
// (db/integrations) plus the auth queries (for the owner/admin check), exactly
// like OrganisationService is over the auth queries.
//
// Access: every operation here is owner/admin only (managing an integration is an
// admin action). The org is always taken from the caller's token, so it can only
// ever act on its own org. The public OAuth callback is the one exception — it
// carries no token and instead trusts the signed `state` it issued at connect.
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
	integrations "github.com/operationfb/accounting-saas/db/integrations"
	"github.com/operationfb/accounting-saas/token"
)

// providerFreeAgent is the provider key in organisation_integrations. v1 has
// exactly one provider; adding Xero later is a new key, not a schema change.
const providerFreeAgent = "freeagent"

// connectStateTTL bounds how long a "connect" link stays valid. The state is a
// signed PASETO carrying the org, so a short life limits the CSRF window and means
// a stale connect link simply fails closed.
const connectStateTTL = 15 * time.Minute

// IntegrationService holds the integrations + auth query sets, the FreeAgent
// client, and the two base URLs the OAuth flow needs (see the fields).
type IntegrationService struct {
	iq          integrations.Querier
	authQueries auth.Querier
	faClient    *freeAgentClient
	tokenMaker  token.Maker

	// apiPublicURL is OUR backend's externally reachable base URL (e.g.
	// https://api.example.com or http://localhost:8080). It builds the OAuth
	// redirect_uri — the address FreeAgent redirects the browser back to — so it
	// is the backend, NOT the frontend.
	apiPublicURL string

	// appBaseURL is the frontend SPA base (e.g. http://localhost:5173). After the
	// callback stores tokens, we send the browser here so the user lands back on
	// the settings screen.
	appBaseURL string
}

// NewIntegrationService is the constructor, called once in main.go.
func NewIntegrationService(iq integrations.Querier, authQueries auth.Querier, faClient *freeAgentClient, tokenMaker token.Maker, apiPublicURL, appBaseURL string) *IntegrationService {
	return &IntegrationService{
		iq:           iq,
		authQueries:  authQueries,
		faClient:     faClient,
		tokenMaker:   tokenMaker,
		apiPublicURL: apiPublicURL,
		appBaseURL:   appBaseURL,
	}
}

// =============================================================================
// DTOs
// =============================================================================

// SaveFreeAgentCredentialsRequest is the body for PUT /api/v1/integrations/freeagent.
// Both fields are the OAuth app credentials from the org's FreeAgent developer
// dashboard. Required — there's nothing to connect without them.
type SaveFreeAgentCredentialsRequest struct {
	ClientID     string `json:"client_id" binding:"required"`
	ClientSecret string `json:"client_secret" binding:"required"`
}

// FreeAgentStatusResponse is what the settings screen renders. It NEVER includes
// the secrets — only whether they are set, whether we're connected, and since when.
type FreeAgentStatusResponse struct {
	HasCredentials bool    `json:"has_credentials"`
	Connected      bool    `json:"connected"`
	ConnectedAt    *string `json:"connected_at,omitempty"`
}

// FreeAgentPushStatusResponse is the per-expense push outcome the SPA renders as a
// "Pushed ✓ / Failed ⚠" badge on an approved expense. State is "pushed"
// (external_expense_ref set), "failed" (push_error set) or "none" (no attempt
// recorded). Connected says whether the org has a live FreeAgent connection at all,
// so the UI can choose between "Pushing…" (connected, awaiting the workflow) and
// showing nothing.
type FreeAgentPushStatusResponse struct {
	State       string  `json:"state"`                  // "pushed" | "failed" | "none"
	ExternalURL *string `json:"external_url,omitempty"` // FreeAgent expense URL, on success
	Error       *string `json:"error,omitempty"`        // last push error message, on failure
	PushedAt    *string `json:"pushed_at,omitempty"`    // RFC3339 — when the push was attempted
	Connected   bool    `json:"connected"`              // org has a connected FreeAgent integration
}

// =============================================================================
// AUTHORIZATION
// =============================================================================

// requireAdmin confirms the caller is an active owner/admin of the org. Managing
// an integration (credentials, connect, disconnect) is an admin action.
func (s *IntegrationService) requireAdmin(ctx context.Context, userID, orgID uuid.UUID) error {
	role, err := authorizeMember(ctx, s.authQueries, userID, orgID)
	if err != nil {
		return err
	}
	if !isOrgAdmin(role) {
		return ErrForbidden("only owners and admins can manage integrations")
	}
	return nil
}

// =============================================================================
// CREDENTIALS + STATUS
// =============================================================================

// SaveCredentials stores (or replaces) the org's FreeAgent OAuth app credentials.
// It does NOT connect — that's the separate interactive step. Returns the status
// so the screen updates to "credentials saved, not yet connected".
func (s *IntegrationService) SaveCredentials(ctx context.Context, authUserID, authOrgID uuid.UUID, req SaveFreeAgentCredentialsRequest) (*FreeAgentStatusResponse, error) {
	if err := s.requireAdmin(ctx, authUserID, authOrgID); err != nil {
		return nil, err
	}
	clientID := strings.TrimSpace(req.ClientID)
	clientSecret := strings.TrimSpace(req.ClientSecret)
	if clientID == "" || clientSecret == "" {
		return nil, ErrValidation("client_id and client_secret are required", nil)
	}

	row, err := s.iq.UpsertIntegrationCredentials(ctx, integrations.UpsertIntegrationCredentialsParams{
		OrganisationID: authOrgID,
		Provider:       providerFreeAgent,
		ClientID:       pgtype.Text{String: clientID, Valid: true},
		ClientSecret:   pgtype.Text{String: clientSecret, Valid: true},
	})
	if err != nil {
		return nil, ErrInternal(err)
	}
	return integrationStatus(row), nil
}

// GetStatus reports whether credentials are saved and whether we're connected.
// A missing row (never configured) is a normal "not set up" state, not an error.
func (s *IntegrationService) GetStatus(ctx context.Context, authUserID, authOrgID uuid.UUID) (*FreeAgentStatusResponse, error) {
	if err := s.requireAdmin(ctx, authUserID, authOrgID); err != nil {
		return nil, err
	}
	row, err := s.iq.GetIntegration(ctx, integrations.GetIntegrationParams{
		OrganisationID: authOrgID,
		Provider:       providerFreeAgent,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return &FreeAgentStatusResponse{}, nil // has_credentials=false, connected=false
		}
		return nil, ErrInternal(err)
	}
	return integrationStatus(row), nil
}

// Disconnect drops the tokens (keeping the credentials so reconnecting is one
// click). Idempotent: clearing a non-existent/already-clear row is a no-op.
func (s *IntegrationService) Disconnect(ctx context.Context, authUserID, authOrgID uuid.UUID) error {
	if err := s.requireAdmin(ctx, authUserID, authOrgID); err != nil {
		return err
	}
	if err := s.iq.ClearIntegrationTokens(ctx, integrations.ClearIntegrationTokensParams{
		OrganisationID: authOrgID,
		Provider:       providerFreeAgent,
	}); err != nil {
		return ErrInternal(err)
	}
	return nil
}

// =============================================================================
// EXPENSE PUSH STATUS (the detail-page badge)
// =============================================================================

// GetExpensePushStatus reports whether — and how — an expense was pushed to the
// org's FreeAgent integration, the data behind the detail-page "Pushed ✓ / Failed
// ⚠" badge. Owner/admin only (observing an integration is an admin action) and
// org-scoped: the push row is reached through the org's own integration_id, so a
// cross-tenant expense id simply finds no row and reads back as "none" — it can
// never leak another org's push. Reuses GetIntegration + GetExpensePushResult, the
// same two queries alreadyPushed chains; no new SQL.
func (s *IntegrationService) GetExpensePushStatus(ctx context.Context, authUserID, authOrgID uuid.UUID, id string) (*FreeAgentPushStatusResponse, error) {
	if err := s.requireAdmin(ctx, authUserID, authOrgID); err != nil {
		return nil, err
	}
	expenseUUID, err := uuid.Parse(id)
	if err != nil {
		return nil, ErrValidation("id is not a valid UUID", err)
	}

	// No integration row (never configured) → nothing pushed, not connected.
	integ, err := s.iq.GetIntegration(ctx, integrations.GetIntegrationParams{
		OrganisationID: authOrgID,
		Provider:       providerFreeAgent,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return &FreeAgentPushStatusResponse{State: "none"}, nil
		}
		return nil, ErrInternal(err)
	}

	resp := &FreeAgentPushStatusResponse{State: "none", Connected: integ.ConnectedAt.Valid}

	res, err := s.iq.GetExpensePushResult(ctx, integrations.GetExpensePushResultParams{
		IntegrationID: integ.ID,
		ExpenseID:     expenseUUID,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return resp, nil // connected (maybe) but never attempted
		}
		return nil, ErrInternal(err)
	}

	// Exactly one of external_expense_ref / push_error is meaningful (see
	// RecordPushResult): a non-empty ref is success, a non-empty error is failure.
	switch {
	case res.ExternalExpenseRef.Valid && res.ExternalExpenseRef.String != "":
		resp.State = "pushed"
		url := res.ExternalExpenseRef.String
		resp.ExternalURL = &url
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
// OAUTH CONNECT FLOW
// =============================================================================

// BuildConnectURL returns the FreeAgent authorize URL the SPA should navigate to.
// It deliberately returns JSON (not a 302): this endpoint is bearer-authed and a
// top-level browser redirect can't carry the SPA's Authorization header, so the
// SPA fetches the URL (token attached) and navigates itself.
func (s *IntegrationService) BuildConnectURL(ctx context.Context, authUserID, authOrgID uuid.UUID) (string, error) {
	if err := s.requireAdmin(ctx, authUserID, authOrgID); err != nil {
		return "", err
	}
	row, err := s.iq.GetIntegration(ctx, integrations.GetIntegrationParams{
		OrganisationID: authOrgID,
		Provider:       providerFreeAgent,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", ErrValidation("save your FreeAgent client_id and client_secret before connecting", nil)
		}
		return "", ErrInternal(err)
	}
	if !row.ClientID.Valid || row.ClientID.String == "" {
		return "", ErrValidation("save your FreeAgent client_id and client_secret before connecting", nil)
	}

	// state = a short-lived signed token carrying the org (and the initiating
	// admin). The public callback verifies it to recover the org and reject
	// forged/stale requests. Reuses the PASETO maker — no new secret.
	state, err := s.tokenMaker.CreateToken(authUserID, authOrgID, connectStateTTL)
	if err != nil {
		return "", ErrInternal(err)
	}
	return s.faClient.authorizeURL(row.ClientID.String, s.redirectURI(), state), nil
}

// HandleCallback completes the connect: verify the signed state → recover the org,
// exchange the code for tokens, store them. It always returns a redirect URL back
// to the SPA (success or ?freeagent=error&reason=...); the second return is a
// non-nil internal cause to LOG (the browser still gets redirected either way).
func (s *IntegrationService) HandleCallback(ctx context.Context, code, state string) (string, error) {
	// Verify the signed state and recover the org. A bad/expired state is a forged
	// or stale link — fail closed with an error redirect, nothing to log.
	payload, err := s.tokenMaker.VerifyToken(state)
	if err != nil {
		return s.callbackErrorURL("invalid_state"), nil
	}
	orgID := payload.OrganisationID

	if strings.TrimSpace(code) == "" {
		return s.callbackErrorURL("missing_code"), nil
	}

	row, err := s.iq.GetIntegration(ctx, integrations.GetIntegrationParams{
		OrganisationID: orgID,
		Provider:       providerFreeAgent,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return s.callbackErrorURL("not_configured"), nil
		}
		return s.callbackErrorURL("internal"), ErrInternal(err)
	}
	if !row.ClientID.Valid || !row.ClientSecret.Valid {
		return s.callbackErrorURL("not_configured"), nil
	}

	tok, err := s.faClient.ExchangeCode(ctx, row.ClientID.String, row.ClientSecret.String, code, s.redirectURI())
	if err != nil {
		// Transient or config failure (e.g. wrong secret, expired code) — log it.
		return s.callbackErrorURL("exchange_failed"), ErrInternal(err)
	}

	if err := s.iq.SetIntegrationTokens(ctx, integrations.SetIntegrationTokensParams{
		OrganisationID: orgID,
		Provider:       providerFreeAgent,
		AccessToken:    pgtype.Text{String: tok.AccessToken, Valid: true},
		RefreshToken:   pgtype.Text{String: tok.RefreshToken, Valid: tok.RefreshToken != ""},
		// Access tokens last ~1h; store the absolute expiry so the refresh check
		// can fire ~5 min early.
		TokenExpiresAt: pgtype.Timestamptz{Time: time.Now().Add(time.Duration(tok.ExpiresIn) * time.Second), Valid: tok.ExpiresIn > 0},
	}); err != nil {
		return s.callbackErrorURL("internal"), ErrInternal(err)
	}

	return s.callbackSuccessURL(), nil
}

// =============================================================================
// URL HELPERS
// =============================================================================

// redirectURI is the backend callback FreeAgent sends the browser back to. It MUST
// match the redirect URI registered in the FreeAgent app and is the backend
// (apiPublicURL), not the frontend.
func (s *IntegrationService) redirectURI() string {
	return strings.TrimRight(s.apiPublicURL, "/") + "/api/v1/freeagent/callback"
}

func (s *IntegrationService) callbackSuccessURL() string {
	return strings.TrimRight(s.appBaseURL, "/") + "/settings/integrations?freeagent=connected"
}

func (s *IntegrationService) callbackErrorURL(reason string) string {
	return strings.TrimRight(s.appBaseURL, "/") + "/settings/integrations?freeagent=error&reason=" + url.QueryEscape(reason)
}

// integrationStatus projects a DB row into the safe status DTO (no secrets).
func integrationStatus(row integrations.OrganisationIntegration) *FreeAgentStatusResponse {
	resp := &FreeAgentStatusResponse{
		HasCredentials: row.ClientID.Valid && row.ClientID.String != "" && row.ClientSecret.Valid,
		Connected:      row.ConnectedAt.Valid,
	}
	if row.ConnectedAt.Valid {
		ts := row.ConnectedAt.Time.Format(time.RFC3339)
		resp.ConnectedAt = &ts
	}
	return resp
}
