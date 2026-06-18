package main

// authz.go
// =============================================================================
// Shared authorisation helper.
//
// Every service (expenses, contacts, attachments, projects, organisation,
// members) starts an operation by checking the caller is an active member of the
// organisation and reading their role. That check was a byte-for-byte copy in all
// six services; this is its single home. Each service keeps a thin authorize()
// method that delegates here, so call sites read unchanged (s.authorize(...)).
// =============================================================================

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	auth "github.com/operationfb/accounting-saas/db/auth"
)

// authorizeMember confirms userID is an ACTIVE member of orgID and returns their
// role. GetMembership does not filter by status, so we check status == "active"
// here. A non-member (no row) or a deactivated member is refused with 403; an
// unexpected DB error becomes 500. The returned role lets callers gate
// admin-only actions (see isOrgAdmin).
func authorizeMember(ctx context.Context, q auth.Querier, userID, orgID uuid.UUID) (auth.OrganisationRole, error) {
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
