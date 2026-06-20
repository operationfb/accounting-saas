package kernel

// dbtx.go (package kernel)
// =============================================================================
// WithTx — the generic transaction wrapper.
//
// It begins a pgx transaction on the pool, hands the raw pgx.Tx to fn, and
// COMMITs if fn returns nil or ROLLBACKs if fn returns an error (or panics-free
// error path). Generic over the tx so EVERY domain can reuse it: each wraps the
// pgx.Tx with its own sqlc *Queries (e.g. expenses.New(tx)) inside fn.
//
// Replaces the per-service withTransaction copies, which each hardcoded one
// domain's Queries type.
// =============================================================================

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// WithTx runs fn inside a PostgreSQL transaction on pool.
//
//  1. Begin a transaction -> a pgx.Tx (a connection pinned to this transaction)
//  2. Call fn(tx) — the caller wraps tx with its own *Queries
//  3. fn returns nil  -> COMMIT (all writes become permanent)
//     fn returns error -> ROLLBACK (all writes are undone) and the error is returned
func WithTx(ctx context.Context, pool *pgxpool.Pool, fn func(pgx.Tx) error) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}

	if err := fn(tx); err != nil {
		// Roll back undoes all writes made inside fn. We return fn's error
		// (what the caller cares about), not the rollback's.
		_ = tx.Rollback(ctx)
		return err
	}

	// Commit makes all writes permanent.
	return tx.Commit(ctx)
}
