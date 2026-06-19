package main

// integration_internal.go
// =============================================================================
// The WORKFLOW-FACING half of IntegrationService: the three operations the
// external Cloud Workflow needs to push an approved expense to FreeAgent. These
// back the OIDC-gated /internal/v1 endpoints (integration_internal_handler.go).
//
//   - TokenForOrg   — hand back a VALID access token (refreshing server-side if
//                     it's near expiry) plus the FreeAgent API base URL.
//   - ExpenseForPush — the provider-neutral expense data, with MONEY already
//                     converted to decimal strings (never float, never in YAML).
//   - RecordPushResult — store the outcome (idempotency + UI status).
//
// All three are org-scoped and resolve the org's integration row themselves, so
// the workflow only ever passes an org id (+ expense id). Provider is FreeAgent
// for now (the only one); generalising is a later concern.
// =============================================================================

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/shopspring/decimal"

	integrations "github.com/operationfb/accounting-saas/db/integrations"
	"github.com/operationfb/accounting-saas/money"
)

// tokenRefreshSkew is how far before expiry we proactively refresh the access
// token, so a token handed to the workflow never expires mid-push.
const tokenRefreshSkew = 5 * time.Minute

// =============================================================================
// DTOs (the JSON the internal endpoints return / accept)
// =============================================================================

// WorkflowTokenResponse is returned by GET /internal/v1/integrations/freeagent/token.
// The workflow uses access_token as the Bearer for FreeAgent calls and
// freeagent_base_url as the host to call (so prod/sandbox can't drift from the
// token, which is only valid against the host it was minted for).
type WorkflowTokenResponse struct {
	AccessToken      string `json:"access_token"`
	FreeAgentBaseURL string `json:"freeagent_base_url"`
	IntegrationID    string `json:"integration_id"`
}

// InternalExpenseResponse is the provider-neutral expense data the workflow maps
// into a FreeAgent payload. Money is decimal strings (pounds), UNSIGNED — the
// FreeAgent sign-negation is a mapping rule done in the workflow. ec_status is
// RAW (UK_NON_EC, …); the workflow maps it to FreeAgent's vocabulary.
type InternalExpenseResponse struct {
	ExpenseID      string  `json:"expense_id"`
	OrganisationID string  `json:"organisation_id"`
	ClaimantEmail  string  `json:"claimant_email"`
	NominalCode    string  `json:"nominal_code"`
	DatedOn        string  `json:"dated_on"` // YYYY-MM-DD
	Description    string  `json:"description"`
	Currency       string  `json:"currency"`
	GrossValue     string  `json:"gross_value"`              // e.g. "100.00" (unsigned pounds)
	SalesTaxValue  string  `json:"sales_tax_value"`          // e.g. "16.67"
	SalesTaxRate   *string `json:"sales_tax_rate,omitempty"` // percent without %, e.g. "20"; omitted when no VAT rate
	ECStatus       string  `json:"ec_status"`                // raw — workflow maps it
	Status         string  `json:"status"`
	AlreadyPushed  bool    `json:"already_pushed"` // idempotency: workflow exits early if true
}

// =============================================================================
// TOKEN VEND (+ server-side refresh)
// =============================================================================

// TokenForOrg returns a valid FreeAgent access token for the org, refreshing it
// server-side if it is within tokenRefreshSkew of expiry. A not-connected org is a
// 409 (conflict). A failed refresh clears the connection (so the settings screen
// shows "needs reconnect") and also returns a 409.
func (s *IntegrationService) TokenForOrg(ctx context.Context, orgID uuid.UUID) (*WorkflowTokenResponse, error) {
	row, err := s.iq.GetIntegration(ctx, integrations.GetIntegrationParams{
		OrganisationID: orgID,
		Provider:       s.provider,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrConflict("FreeAgent is not connected for this organisation")
		}
		return nil, ErrInternal(err)
	}
	if !row.ConnectedAt.Valid || !row.AccessToken.Valid {
		return nil, ErrConflict("FreeAgent is not connected for this organisation")
	}

	accessToken := row.AccessToken.String

	// Refresh if the access token is near (or past) expiry.
	if row.TokenExpiresAt.Valid && time.Until(row.TokenExpiresAt.Time) < tokenRefreshSkew {
		if !row.RefreshToken.Valid {
			return nil, ErrConflict("FreeAgent connection is missing its refresh token — please reconnect")
		}
		// The refresh uses the GLOBAL app credentials (client_id/client_secret).
		// If they're gone the integration is unconfigured — surface "reconnect"
		// rather than a 500.
		creds, cerr := s.iq.GetProviderCredentials(ctx, s.provider)
		if cerr != nil {
			if errors.Is(cerr, pgx.ErrNoRows) {
				return nil, ErrConflict("FreeAgent is not configured — please reconnect")
			}
			return nil, ErrInternal(cerr)
		}
		tok, rerr := s.faClient.RefreshToken(ctx, creds.ClientID, creds.ClientSecret, row.RefreshToken.String)
		if rerr != nil {
			// A failed refresh means the connection is broken (revoked/expired).
			// Clear it so the UI surfaces "needs reconnect"; the push fails this time.
			_ = s.iq.ClearIntegrationTokens(ctx, integrations.ClearIntegrationTokensParams{
				OrganisationID: orgID,
				Provider:       s.provider,
			})
			return nil, ErrConflict("FreeAgent token refresh failed — please reconnect")
		}

		// FreeAgent returns a new refresh token on refresh; keep the old one only if
		// the response omitted it (defensive — FreeAgent does rotate it).
		newRefresh := row.RefreshToken.String
		if tok.RefreshToken != "" {
			newRefresh = tok.RefreshToken
		}
		if err := s.iq.SetIntegrationTokens(ctx, integrations.SetIntegrationTokensParams{
			OrganisationID: orgID,
			Provider:       s.provider,
			AccessToken:    pgtype.Text{String: tok.AccessToken, Valid: true},
			RefreshToken:   pgtype.Text{String: newRefresh, Valid: newRefresh != ""},
			TokenExpiresAt: pgtype.Timestamptz{Time: time.Now().Add(time.Duration(tok.ExpiresIn) * time.Second), Valid: tok.ExpiresIn > 0},
		}); err != nil {
			return nil, ErrInternal(err)
		}
		accessToken = tok.AccessToken
	}

	return &WorkflowTokenResponse{
		AccessToken:      accessToken,
		FreeAgentBaseURL: s.faClient.apiBaseURL(),
		IntegrationID:    row.ID.String(),
	}, nil
}

// =============================================================================
// EXPENSE FOR PUSH
// =============================================================================

// ExpenseForPush returns the data the workflow needs to build a FreeAgent payload,
// org-scoped. Money columns (pence) are converted to decimal pound strings HERE
// (in tested Go), so no float arithmetic ever happens in the workflow YAML.
func (s *IntegrationService) ExpenseForPush(ctx context.Context, orgID, expenseID uuid.UUID) (*InternalExpenseResponse, error) {
	row, err := s.iq.GetExpenseForPush(ctx, integrations.GetExpenseForPushParams{
		ExpenseID:      expenseID,
		OrganisationID: orgID,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound("expense", expenseID.String())
		}
		return nil, ErrInternal(err)
	}

	alreadyPushed, err := s.alreadyPushed(ctx, orgID, expenseID)
	if err != nil {
		return nil, err
	}

	resp := &InternalExpenseResponse{
		ExpenseID:      row.ExpenseID.String(),
		OrganisationID: row.OrganisationID.String(),
		ClaimantEmail:  row.ClaimantEmail,
		NominalCode:    row.NominalCode,
		DatedOn:        row.DatedOn.Time.Format("2006-01-02"),
		Description:    row.Description,
		Currency:       row.Currency,
		GrossValue:     money.MinorToPounds(int64(row.GrossValueMinor)),
		SalesTaxValue:  money.MinorToPounds(int64(row.VatValueMinor)),
		ECStatus:       row.EcStatus,
		Status:         row.Status,
		AlreadyPushed:  alreadyPushed,
	}
	// sales_tax_rate as a plain percent string ("20", "17.5") — NOT money.BpsToPercent
	// (which appends "%"). Omitted when the expense has no VAT rate.
	if row.VatRateBps.Valid {
		rate := decimal.NewFromInt(int64(row.VatRateBps.Int32)).Div(decimal.NewFromInt(100)).String()
		resp.SalesTaxRate = &rate
	}
	return resp, nil
}

// alreadyPushed reports whether this expense already has a SUCCESSFUL push row for
// the org's FreeAgent integration (a non-empty external_expense_ref). A
// not-configured org or a never-attempted expense is simply "not pushed".
func (s *IntegrationService) alreadyPushed(ctx context.Context, orgID, expenseID uuid.UUID) (bool, error) {
	integ, err := s.iq.GetIntegration(ctx, integrations.GetIntegrationParams{
		OrganisationID: orgID,
		Provider:       s.provider,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return false, nil
		}
		return false, ErrInternal(err)
	}
	res, err := s.iq.GetExpensePushResult(ctx, integrations.GetExpensePushResultParams{
		IntegrationID: integ.ID,
		ExpenseID:     expenseID,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return false, nil
		}
		return false, ErrInternal(err)
	}
	return res.ExternalExpenseRef.Valid && res.ExternalExpenseRef.String != "", nil
}

// =============================================================================
// PUSH RESULT
// =============================================================================

// RecordPushResult stores the outcome of a push (idempotent upsert keyed on
// integration+expense). Exactly one of externalRef / pushErr is meaningful: a
// non-empty externalRef means success, a non-empty pushErr means failure.
func (s *IntegrationService) RecordPushResult(ctx context.Context, orgID, expenseID uuid.UUID, externalRef, pushErr string) error {
	integ, err := s.iq.GetIntegration(ctx, integrations.GetIntegrationParams{
		OrganisationID: orgID,
		Provider:       s.provider,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound("integration", orgID.String())
		}
		return ErrInternal(err)
	}
	if err := s.iq.UpsertExpensePushResult(ctx, integrations.UpsertExpensePushResultParams{
		IntegrationID:      integ.ID,
		ExpenseID:          expenseID,
		ExternalExpenseRef: pgtype.Text{String: externalRef, Valid: externalRef != ""},
		PushError:          pgtype.Text{String: pushErr, Valid: pushErr != ""},
	}); err != nil {
		return ErrInternal(err)
	}
	return nil
}
