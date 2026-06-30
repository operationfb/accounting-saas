package categories

// provision.go
// =============================================================================
// Provisioning a NEW organisation's chart of accounts from the global/country
// chart_template. The GL (and the explain picker, bills, expenses) all post against
// `categories`, so an org with no chart can't use them — every org must be provisioned
// on creation. This is the single entry point; call it inside the org-creation tx.
// =============================================================================

import (
	"context"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	categoriesdb "github.com/operationfb/accounting-saas/db/categories"
)

// ProvisionChart seeds an organisation's chart of accounts from chart_template — the
// org's country-specific template if one exists, else the global fallback. Idempotent
// (safe to re-run). Pass a tx-bound querier so it commits with the org creation.
// countryCode is organisations.country_code; "" uses the global fallback.
func ProvisionChart(ctx context.Context, q categoriesdb.Querier, orgID uuid.UUID, countryCode string) error {
	return q.ProvisionCategoriesForOrg(ctx, categoriesdb.ProvisionCategoriesForOrgParams{
		OrganisationID: orgID,
		OrgCountry:     pgtype.Text{String: countryCode, Valid: countryCode != ""},
	})
}
