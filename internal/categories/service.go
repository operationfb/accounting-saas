package categories

// service.go
// =============================================================================
// Service — the reconcile reference data behind "explaining" a bank transaction:
// the explanation TYPES (the FreeAgent dropdown) and, per type, the offered
// Chart-of-Accounts categories (resolved against the org's company_type).
//
// A thin, read-only service over the generated db/categories package (aliased
// categoriesdb to avoid the package-name clash), structured like internal/members:
//
//   HTTP handler (handler.go)
//     ↓  Service (this file)
//     ↓  categoriesdb.Queries + auth.Querier
//     ↓  PostgreSQL
//
// Any active member may read (the pickers are needed to explain). The org's
// company_type (read via auth.GetOrganisation) selects the Ltd vs sole-trader
// options in the mapping.
// =============================================================================

import (
	"context"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	auth "github.com/operationfb/accounting-saas/db/auth"
	categoriesdb "github.com/operationfb/accounting-saas/db/categories"
	"github.com/operationfb/accounting-saas/internal/kernel"
)

// Service holds the categories query set + the auth queries (for membership authz
// and the org's company_type). No pool/transaction — these are read-only lookups.
type Service struct {
	queries     *categoriesdb.Queries
	authQueries auth.Querier
}

// NewService is the constructor, called once in main.go.
func NewService(queries *categoriesdb.Queries, authQueries auth.Querier) *Service {
	return &Service{queries: queries, authQueries: authQueries}
}

// SupportedEntityLinks is the v1 explain whitelist: category types (NONE),
// transfers (BANK_ACCOUNT), user payments (USER), capital disposal (CAPITAL_ASSET,
// a category pick for now), and Invoice Receipt (INVOICE — settles a sent sales
// invoice) and Bill Payment (BILL — settles an unpaid purchase bill). The remaining
// future-entity types (CREDIT_NOTE/HP) are not explainable until their modules land.
// Exported so the banking explain service gates on the EXACT same set.
var SupportedEntityLinks = map[string]bool{
	"NONE":          true,
	"BANK_ACCOUNT":  true,
	"USER":          true,
	"CAPITAL_ASSET": true,
	"INVOICE":       true,
	"BILL":          true,
}

// authorize confirms the caller is an active member (delegates to the kernel).
func (s *Service) authorize(ctx context.Context, userID, orgID uuid.UUID) (auth.OrganisationRole, error) {
	return kernel.AuthorizeMember(ctx, s.authQueries, userID, orgID)
}

// ListTransactionTypes returns the 18 explanation types (global reference), each
// flagged supported/unsupported for v1. Any active member.
func (s *Service) ListTransactionTypes(ctx context.Context, authUserID, authOrgID uuid.UUID) ([]*TransactionTypeResponse, error) {
	if _, err := s.authorize(ctx, authUserID, authOrgID); err != nil {
		return nil, err
	}
	rows, err := s.queries.ListTransactionTypes(ctx)
	if err != nil {
		return nil, kernel.ErrInternal(err)
	}
	out := make([]*TransactionTypeResponse, 0, len(rows))
	for _, t := range rows {
		out = append(out, &TransactionTypeResponse{
			Code:       t.Code,
			Name:       t.Name,
			Direction:  t.Direction,
			EntityLink: t.EntityLink,
			Supported:  SupportedEntityLinks[t.EntityLink],
		})
	}
	return out, nil
}

// ListCategoriesForType returns the CoA accounts a type offers, resolved against
// the caller's org + its company_type. Any active member.
func (s *Service) ListCategoriesForType(ctx context.Context, authUserID, authOrgID uuid.UUID, typeCode string) ([]*CategoryResponse, error) {
	if _, err := s.authorize(ctx, authUserID, authOrgID); err != nil {
		return nil, err
	}
	rows, err := s.queries.ListCategoriesForType(ctx, categoriesdb.ListCategoriesForTypeParams{
		OrganisationID:      authOrgID,
		TransactionTypeCode: typeCode,
		CompanyType:         s.orgCompanyType(ctx, authOrgID),
	})
	if err != nil {
		return nil, kernel.ErrInternal(err)
	}
	out := make([]*CategoryResponse, 0, len(rows))
	for _, r := range rows {
		out = append(out, categoryToResponse(r.Category, r.DisplayLabel))
	}
	return out, nil
}

// orgCompanyType reads the org's company_type ('' if unset → only the 'ALL'
// mapping rows match). Best-effort: a read error degrades to '' rather than failing
// the picker.
func (s *Service) orgCompanyType(ctx context.Context, orgID uuid.UUID) string {
	org, err := s.authQueries.GetOrganisation(ctx, orgID)
	if err != nil || !org.CompanyType.Valid {
		return ""
	}
	return org.CompanyType.String
}

// categoryToResponse maps a CoA row + the mapping's optional label override into
// the API shape (the label wins over the CoA account name where set).
func categoryToResponse(c categoriesdb.Category, displayLabel pgtype.Text) *CategoryResponse {
	name := c.Name
	if displayLabel.Valid && displayLabel.String != "" {
		name = displayLabel.String
	}
	return &CategoryResponse{
		ID:          c.ID.String(),
		NominalCode: c.NominalCode,
		Name:        name,
		AccountType: c.AccountType,
		ApiGroup:    kernel.NullTextToPtr(c.ApiGroup),
		DefaultVat:  kernel.NullTextToPtr(c.DefaultVat),
	}
}
