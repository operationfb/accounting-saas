package integrations

// workflow.go
// =============================================================================
// The WORKFLOW-FACING half of the Service: the operations the external Cloud
// Workflow needs to push an approved expense. These back the OIDC-gated
// /internal/v1 endpoints (internal_handler.go).
//
//   - TokenForOrg     — a VALID access token (refreshing server-side if near
//                       expiry) plus the provider API base URL.
//   - ExpenseForPush  — provider-neutral expense data, MONEY already converted to
//                       decimal strings (never float, never in YAML).
//   - RecordPushResult — store the outcome (idempotency + UI status).
//
// All are org-scoped and resolve the org's integration row themselves.
// =============================================================================

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/shopspring/decimal"

	integrationsdb "github.com/operationfb/accounting-saas/db/integrations"
	"github.com/operationfb/accounting-saas/internal/kernel"
	"github.com/operationfb/accounting-saas/money"
)

// tokenRefreshSkew is how far before expiry we proactively refresh the access
// token, so a token handed to the workflow never expires mid-push.
const tokenRefreshSkew = 5 * time.Minute

// =============================================================================
// DTOs (the JSON the internal endpoints return / accept)
// =============================================================================

// WorkflowTokenResponse is returned by the token endpoint. The workflow uses
// AccessToken as the Bearer and APIBaseURL as the host to call (so prod/sandbox
// can't drift from the token, valid only against the host it was minted for).
//
// NOTE: the api_base_url json key is currently "freeagent_base_url" — the existing
// workflow contract. Generalising it (+ the workflow YAML) is a later cleanup.
type WorkflowTokenResponse struct {
	AccessToken   string `json:"access_token"`
	APIBaseURL    string `json:"freeagent_base_url"`
	IntegrationID string `json:"integration_id"`
}

// InternalExpenseResponse is the provider-neutral expense data the workflow maps
// into a provider payload. Money is decimal strings (pounds), UNSIGNED — any sign
// convention is a mapping rule done in the workflow. ec_status is RAW.
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
	SalesTaxRate   *string `json:"sales_tax_rate,omitempty"` // percent without %, e.g. "20"
	ECStatus       string  `json:"ec_status"`                // raw — workflow maps it
	Status         string  `json:"status"`
	AlreadyPushed  bool    `json:"already_pushed"` // idempotency: workflow exits early if true
}

// =============================================================================
// TOKEN VEND (+ server-side refresh)
// =============================================================================

// TokenForOrg returns a valid access token for the org, refreshing it server-side
// if it is within tokenRefreshSkew of expiry. A not-connected org is a 409. A
// failed refresh clears the connection (UI shows "needs reconnect") and 409s.
func (s *Service) TokenForOrg(ctx context.Context, orgID uuid.UUID) (*WorkflowTokenResponse, error) {
	row, err := s.iq.GetIntegration(ctx, integrationsdb.GetIntegrationParams{
		OrganisationID: orgID,
		Provider:       s.provider,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, kernel.ErrConflict("this integration is not connected for this organisation")
		}
		return nil, kernel.ErrInternal(err)
	}
	if !row.ConnectedAt.Valid || !row.AccessToken.Valid {
		return nil, kernel.ErrConflict("this integration is not connected for this organisation")
	}

	accessToken := row.AccessToken.String

	if row.TokenExpiresAt.Valid && time.Until(row.TokenExpiresAt.Time) < tokenRefreshSkew {
		if !row.RefreshToken.Valid {
			return nil, kernel.ErrConflict("this integration is missing its refresh token — please reconnect")
		}
		creds, cerr := s.iq.GetProviderCredentials(ctx, s.provider)
		if cerr != nil {
			if errors.Is(cerr, pgx.ErrNoRows) {
				return nil, kernel.ErrConflict("this integration is not configured — please reconnect")
			}
			return nil, kernel.ErrInternal(cerr)
		}
		tok, rerr := s.oauth.RefreshToken(ctx, creds.ClientID, creds.ClientSecret, row.RefreshToken.String)
		if rerr != nil {
			// A failed refresh means the connection is broken (revoked/expired).
			// Clear it so the UI surfaces "needs reconnect"; the push fails this time.
			_ = s.iq.ClearIntegrationTokens(ctx, integrationsdb.ClearIntegrationTokensParams{
				OrganisationID: orgID,
				Provider:       s.provider,
			})
			return nil, kernel.ErrConflict("token refresh failed — please reconnect")
		}

		// Keep the old refresh token only if the response omitted a new one
		// (defensive — providers typically rotate it).
		newRefresh := row.RefreshToken.String
		if tok.RefreshToken != "" {
			newRefresh = tok.RefreshToken
		}
		if err := s.iq.SetIntegrationTokens(ctx, integrationsdb.SetIntegrationTokensParams{
			OrganisationID: orgID,
			Provider:       s.provider,
			AccessToken:    pgtype.Text{String: tok.AccessToken, Valid: true},
			RefreshToken:   pgtype.Text{String: newRefresh, Valid: newRefresh != ""},
			TokenExpiresAt: pgtype.Timestamptz{Time: time.Now().Add(time.Duration(tok.ExpiresIn) * time.Second), Valid: tok.ExpiresIn > 0},
		}); err != nil {
			return nil, kernel.ErrInternal(err)
		}
		accessToken = tok.AccessToken
	}

	return &WorkflowTokenResponse{
		AccessToken:   accessToken,
		APIBaseURL:    s.oauth.APIBaseURL(),
		IntegrationID: row.ID.String(),
	}, nil
}

// =============================================================================
// EXPENSE FOR PUSH
// =============================================================================

// ExpenseForPush returns the data the workflow needs to build a payload,
// org-scoped. Money columns (pence) are converted to decimal pound strings HERE
// (tested Go), so no float arithmetic ever happens in the workflow YAML.
func (s *Service) ExpenseForPush(ctx context.Context, orgID, expenseID uuid.UUID) (*InternalExpenseResponse, error) {
	row, err := s.iq.GetExpenseForPush(ctx, integrationsdb.GetExpenseForPushParams{
		ExpenseID:      expenseID,
		OrganisationID: orgID,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, kernel.ErrNotFound("expense", expenseID.String())
		}
		return nil, kernel.ErrInternal(err)
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
	// sales_tax_rate as a plain percent string ("20", "17.5"). Omitted when no rate.
	if row.VatRateBps.Valid {
		rate := decimal.NewFromInt(int64(row.VatRateBps.Int32)).Div(decimal.NewFromInt(100)).String()
		resp.SalesTaxRate = &rate
	}
	return resp, nil
}

// alreadyPushed reports whether this expense already has a SUCCESSFUL push row.
func (s *Service) alreadyPushed(ctx context.Context, orgID, expenseID uuid.UUID) (bool, error) {
	integ, err := s.iq.GetIntegration(ctx, integrationsdb.GetIntegrationParams{
		OrganisationID: orgID,
		Provider:       s.provider,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return false, nil
		}
		return false, kernel.ErrInternal(err)
	}
	res, err := s.iq.GetExpensePushResult(ctx, integrationsdb.GetExpensePushResultParams{
		IntegrationID: integ.ID,
		ExpenseID:     expenseID,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return false, nil
		}
		return false, kernel.ErrInternal(err)
	}
	return res.ExternalExpenseRef.Valid && res.ExternalExpenseRef.String != "", nil
}

// =============================================================================
// PUSH RESULT
// =============================================================================

// RecordPushResult stores the outcome of a push (idempotent upsert keyed on
// integration+expense). Exactly one of externalRef / pushErr is meaningful.
func (s *Service) RecordPushResult(ctx context.Context, orgID, expenseID uuid.UUID, externalRef, pushErr string) error {
	integ, err := s.iq.GetIntegration(ctx, integrationsdb.GetIntegrationParams{
		OrganisationID: orgID,
		Provider:       s.provider,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return kernel.ErrNotFound("integration", orgID.String())
		}
		return kernel.ErrInternal(err)
	}
	if err := s.iq.UpsertExpensePushResult(ctx, integrationsdb.UpsertExpensePushResultParams{
		IntegrationID:      integ.ID,
		ExpenseID:          expenseID,
		ExternalExpenseRef: pgtype.Text{String: externalRef, Valid: externalRef != ""},
		PushError:          pgtype.Text{String: pushErr, Valid: pushErr != ""},
	}); err != nil {
		return kernel.ErrInternal(err)
	}
	return nil
}
