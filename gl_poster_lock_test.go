package main

// gl_poster_lock_test.go
// =============================================================================
// The append-only ledger has no source uniqueness index, so the poster takes a
// transaction-scoped advisory lock (LockSource) on the source identity to stop two
// transactions double-posting the SAME source. These tests prove the lock serialises
// same-key callers and leaves different keys independent — deterministically, no
// reliance on timing races.
// =============================================================================

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	dbledger "github.com/operationfb/accounting-saas/db/ledger"
)

// TestGLAppendOnlyGuard proves the database enforces the append-only invariant: once a
// journal entry/line exists, UPDATE and DELETE are rejected by the guard triggers
// (trg_gl_*_no_update/no_delete in db/schema/ledger_schema.sql). It also exercises the
// positive control — the maintenance bypass used by test teardown CAN still purge the
// rows — so we know the guard is strict but not a footgun for cleanup.
func TestGLAppendOnlyGuard(t *testing.T) {
	ts := newTestServer(t)
	ctx := context.Background()

	accountID := randomCategoryUUID(t, ts.pool)

	// Post a minimal balanced entry directly: one zero-amount line keeps the balance
	// trigger happy (Σ base_amount_minor = 0). INSERT is allowed — the guard only
	// blocks UPDATE/DELETE.
	var entryID string
	if err := ts.pool.QueryRow(ctx,
		`INSERT INTO gl_journal_entries (organisation_id, entry_date, base_currency, source_type, narrative)
		 VALUES ($1, CURRENT_DATE, 'GBP', 'MANUAL', 'append-only guard test')
		 RETURNING id`, devOrgID).Scan(&entryID); err != nil {
		t.Fatalf("insert entry: %v", err)
	}
	// Teardown via the bypass helper (a plain DELETE would hit the guard).
	t.Cleanup(func() { purgeGLEntries(ctx, t, ts.pool, `id = $1`, entryID) })

	var lineID string
	if err := ts.pool.QueryRow(ctx,
		`INSERT INTO gl_journal_lines (journal_entry_id, organisation_id, account_id, currency, amount_minor, base_amount_minor)
		 VALUES ($1, $2, $3, 'GBP', 0, 0)
		 RETURNING id`, entryID, devOrgID, accountID).Scan(&lineID); err != nil {
		t.Fatalf("insert line: %v", err)
	}

	// Every mutation must be REJECTED with an "append-only" error.
	mutations := []struct {
		name string
		sql  string
		arg  string
	}{
		{"update entry", `UPDATE gl_journal_entries SET narrative = 'tampered' WHERE id = $1`, entryID},
		{"delete entry", `DELETE FROM gl_journal_entries WHERE id = $1`, entryID},
		{"update line", `UPDATE gl_journal_lines SET amount_minor = 999 WHERE id = $1`, lineID},
		{"delete line", `DELETE FROM gl_journal_lines WHERE id = $1`, lineID},
	}
	for _, m := range mutations {
		if _, err := ts.pool.Exec(ctx, m.sql, m.arg); err == nil {
			t.Errorf("%s: expected the append-only guard to block it, but it succeeded", m.name)
		} else if !strings.Contains(err.Error(), "append-only") {
			t.Errorf("%s: blocked, but error did not mention append-only: %v", m.name, err)
		}
	}

	// Sanity: the row survived every blocked mutation.
	var n int
	if err := ts.pool.QueryRow(ctx, `SELECT count(*) FROM gl_journal_entries WHERE id = $1`, entryID).Scan(&n); err != nil {
		t.Fatalf("recount entry: %v", err)
	}
	if n != 1 {
		t.Fatalf("entry should survive blocked mutations, got count=%d", n)
	}

	// Positive control: the maintenance bypass CAN purge it (proves teardown works).
	purgeGLEntries(ctx, t, ts.pool, `id = $1`, entryID)
	if err := ts.pool.QueryRow(ctx, `SELECT count(*) FROM gl_journal_entries WHERE id = $1`, entryID).Scan(&n); err != nil {
		t.Fatalf("recount after bypass: %v", err)
	}
	if n != 0 {
		t.Errorf("bypass purge should have removed the entry, got count=%d", n)
	}
}

func TestGLSourceLockSerialisesSameKey(t *testing.T) {
	ts := newTestServer(t)
	ctx := context.Background()
	lq := dbledger.New(ts.pool)
	key := "gl:test:" + uuid.NewString()

	// tx1 takes the lock and holds it (no commit yet).
	tx1, err := ts.pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin tx1: %v", err)
	}
	defer tx1.Rollback(ctx)
	if err := lq.WithTx(tx1).LockSource(ctx, key); err != nil {
		t.Fatalf("tx1 lock: %v", err)
	}

	// tx2 (separate connection) tries the SAME key — it must block until tx1 ends.
	acquired := make(chan error, 1)
	go func() {
		bg := context.Background()
		tx2, err := ts.pool.Begin(bg)
		if err != nil {
			acquired <- err
			return
		}
		defer tx2.Rollback(bg)
		acquired <- lq.WithTx(tx2).LockSource(bg, key)
	}()

	select {
	case err := <-acquired:
		t.Fatalf("tx2 acquired the same-source lock while tx1 held it (err=%v)", err)
	case <-time.After(250 * time.Millisecond):
		// expected: still blocked
	}

	// Releasing tx1 lets tx2 acquire.
	if err := tx1.Commit(ctx); err != nil {
		t.Fatalf("commit tx1: %v", err)
	}
	select {
	case err := <-acquired:
		if err != nil {
			t.Errorf("tx2 lock after tx1 released: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("tx2 did not acquire the lock after tx1 released it")
	}
}

func TestGLSourceLockDifferentKeysIndependent(t *testing.T) {
	ts := newTestServer(t)
	ctx := context.Background()
	lq := dbledger.New(ts.pool)

	tx1, err := ts.pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin tx1: %v", err)
	}
	defer tx1.Rollback(ctx)
	if err := lq.WithTx(tx1).LockSource(ctx, "gl:test:"+uuid.NewString()); err != nil {
		t.Fatalf("tx1 lock: %v", err)
	}

	// A DIFFERENT key must acquire immediately (different sources never block).
	done := make(chan error, 1)
	go func() {
		bg := context.Background()
		tx2, err := ts.pool.Begin(bg)
		if err != nil {
			done <- err
			return
		}
		defer tx2.Rollback(bg)
		done <- lq.WithTx(tx2).LockSource(bg, "gl:test:"+uuid.NewString())
	}()

	select {
	case err := <-done:
		if err != nil {
			t.Errorf("different-key lock: %v", err)
		}
	case <-time.After(1 * time.Second):
		t.Fatal("a different-key advisory lock should not block")
	}
}
