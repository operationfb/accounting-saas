package kernel

// authz.go (package kernel)
// =============================================================================
// Shared authorisation helpers.
//
// Every service starts an operation by checking the caller is an active member
// of the organisation and reading their role. That check lives here once
// (AuthorizeMember); IsOrgAdmin then gates owner/admin-only actions. Each service
// keeps a thin authorize() method that delegates to AuthorizeMember.
// =============================================================================

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	auth "github.com/operationfb/accounting-saas/db/auth"
)

// AuthorizeMember confirms userID is an ACTIVE member of orgID and returns their
// role. GetMembership does not filter by status, so we check status == "active"
// here. A non-member (no row) or a deactivated member is refused with 403; an
// unexpected DB error becomes 500. The returned role lets callers gate
// admin-only actions (see IsOrgAdmin).
func AuthorizeMember(ctx context.Context, q auth.Querier, userID, orgID uuid.UUID) (auth.OrganisationRole, error) {
	m, err := q.GetMembership(ctx, auth.GetMembershipParams{
		OrganisationID: orgID,
		UserID:         userID,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", ErrForbidden("you are not a member of this organisation")
		}
		return "", ErrInternal(err)
	}
	if m.Status != "active" {
		return "", ErrForbidden("your organisation membership is not active")
	}
	return m.Role, nil
}

// IsOrgAdmin reports whether a role may perform owner/admin-only actions (and read
// ALL of an organisation's data). Per product decision this is owner and admin
// only; member/accountant/read_only are limited to their own records.
func IsOrgAdmin(role auth.OrganisationRole) bool {
	return role == auth.OrganisationRoleOwner || role == auth.OrganisationRoleAdmin
}
