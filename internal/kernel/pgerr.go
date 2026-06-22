package kernel

// pgerr.go
// =============================================================================
// PostgreSQL error-class helpers shared across domains. These classify a pgx
// error by its SQLSTATE so a service can turn an expected constraint failure
// into a domain decision (dedupe, conflict) instead of leaking a raw DB error.
// =============================================================================

import (
	"errors"

	"github.com/jackc/pgx/v5/pgconn"
)

// IsUniqueViolation reports whether err is a PostgreSQL unique-constraint
// violation (SQLSTATE 23505). Used by any domain that dedupes on a unique index
// (email-inbox Message-Id, banking feed/primary-account, …).
func IsUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}
