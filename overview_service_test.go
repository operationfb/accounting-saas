package main

// overview_service_test.go
// =============================================================================
// Service-level integration tests for the Overview dashboard (internal/overview),
// against real PostgreSQL — same harness as banking_service_test.go. The Cashflow
// card aggregates bank_transactions (signed pence) into 12 monthly money-in/out
// buckets; these tests cover the bucketing, the window totals + net Balance, the
// pence→pounds conversion at the boundary, the exclusions (soft-deleted + outside
// the 12-month window), authorisation, and multi-tenant isolation.
//
// The service isn't wired into testServer, so we build it directly from ts.pool
// (auth queries for authorisation + the db/overview read queries). Each test uses
// a FRESH ephemeral org (newOrgWithOwner) and hard-deletes its bank rows in
// cleanup so the shared dev DB stays clean.
// =============================================================================

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	authdb "github.com/operationfb/accounting-saas/db/auth"
	dboverview "github.com/operationfb/accounting-saas/db/overview"
	overview "github.com/operationfb/accounting-saas/internal/overview"
)

// seedAccount inserts a bank account (with the given opening balance in pence) for
// an org and returns its id. Cleaned up (with its transactions) before the org row
// it points at is removed.
func seedAccount(t *testing.T, ts *testServer, orgID, userID string, openingMinor int64) string {
	t.Helper()
	id := uuid.NewString()
	if _, err := ts.pool.Exec(context.Background(), `
		INSERT INTO bank_accounts (id, organisation_id, created_by_user_id, name, opening_balance_minor)
		VALUES ($1, $2, $3, 'Overview Test Account', $4)`, id, orgID, userID, openingMinor); err != nil {
		t.Fatalf("seedAccount: %v", err)
	}
	t.Cleanup(func() {
		ctx := context.Background()
		_, _ = ts.pool.Exec(ctx, `DELETE FROM bank_transactions WHERE bank_account_id = $1`, id)
		_, _ = ts.pool.Exec(ctx, `DELETE FROM bank_accounts WHERE id = $1`, id)
	})
	return id
}

// seedTxn inserts one bank transaction dated `on` with the given SIGNED amount
// (+ in / - out). `deleted` sets deleted_at so the read path must exclude it.
func seedTxn(t *testing.T, ts *testServer, orgID, acctID string, on time.Time, amountMinor int64, deleted bool) {
	t.Helper()
	var del any
	if deleted {
		del = time.Now()
	}
	if _, err := ts.pool.Exec(context.Background(), `
		INSERT INTO bank_transactions (organisation_id, bank_account_id, dated_on, amount_minor, status, source, deleted_at)
		VALUES ($1, $2, $3, $4, 'unexplained', 'manual', $5)`,
		orgID, acctID, on, amountMinor, del); err != nil {
		t.Fatalf("seedTxn: %v", err)
	}
}

func TestOverviewCashflow(t *testing.T) {
	ts := newTestServer(t)
	t.Cleanup(func() { ts.pool.Close() })
	ctx := context.Background()
	svc := overview.NewService(authdb.New(ts.pool), dboverview.New(ts.pool))

	// Month anchors derived from "today" the same way the query does. firstOfThis is
	// safely inside the current month; lastOfPrev is inside the previous month —
	// both avoid day-of-month overflow from naive AddDate.
	now := time.Now()
	firstOfThis := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
	lastOfPrev := firstOfThis.AddDate(0, 0, -1)
	outOfWindow := firstOfThis.AddDate(0, -13, 0) // 13 months back → outside the 12-month window
	const isoDay = "2006-01-02"

	t.Run("buckets, totals, conversion and exclusions", func(t *testing.T) {
		t.Parallel()
		org, user := newOrgWithOwner(t, ts)
		acct := seedAccount(t, ts, org, user, 0)

		// Current month: £1000 in, £400 out.
		seedTxn(t, ts, org, acct, firstOfThis, 100000, false)
		seedTxn(t, ts, org, acct, firstOfThis, -40000, false)
		// Previous month: £250 in, £100 out.
		seedTxn(t, ts, org, acct, lastOfPrev, 25000, false)
		seedTxn(t, ts, org, acct, lastOfPrev, -10000, false)
		// Excluded: outside the window, and a soft-deleted row this month.
		seedTxn(t, ts, org, acct, outOfWindow, 9999999, false)
		seedTxn(t, ts, org, acct, firstOfThis, 8888888, true)

		res, err := svc.Cashflow(ctx, mustUUID(t, user), mustUUID(t, org))
		if err != nil {
			t.Fatalf("Cashflow: %v", err)
		}

		// Always exactly 12 zero-filled buckets, oldest→newest.
		if len(res.Months) != 12 {
			t.Fatalf("len(Months) = %d, want 12", len(res.Months))
		}
		last := res.Months[11]
		prev := res.Months[10]
		if last.Month != firstOfThis.Format(isoDay) {
			t.Errorf("last bucket month = %q, want %q", last.Month, firstOfThis.Format(isoDay))
		}
		if last.Incoming != "1000.00" || last.Outgoing != "400.00" {
			t.Errorf("current month = in %q/out %q, want 1000.00/400.00", last.Incoming, last.Outgoing)
		}
		if prev.Incoming != "250.00" || prev.Outgoing != "100.00" {
			t.Errorf("previous month = in %q/out %q, want 250.00/100.00", prev.Incoming, prev.Outgoing)
		}

		// Window totals exclude the soft-deleted + out-of-window rows. Balance is the
		// net (in − out): 1250 − 500 = 750.
		if res.Incoming != "1250.00" || res.Outgoing != "500.00" || res.Balance != "750.00" {
			t.Errorf("totals = in %q/out %q/bal %q, want 1250.00/500.00/750.00",
				res.Incoming, res.Outgoing, res.Balance)
		}
	})

	t.Run("multi-tenant isolation", func(t *testing.T) {
		t.Parallel()
		orgA, userA := newOrgWithOwner(t, ts)
		acctA := seedAccount(t, ts, orgA, userA, 0)
		seedTxn(t, ts, orgA, acctA, firstOfThis, 50000, false) // £500 in

		orgB, userB := newOrgWithOwner(t, ts)
		acctB := seedAccount(t, ts, orgB, userB, 0)
		seedTxn(t, ts, orgB, acctB, firstOfThis, 70000, false) // £700 in

		resA, err := svc.Cashflow(ctx, mustUUID(t, userA), mustUUID(t, orgA))
		if err != nil {
			t.Fatalf("Cashflow A: %v", err)
		}
		resB, err := svc.Cashflow(ctx, mustUUID(t, userB), mustUUID(t, orgB))
		if err != nil {
			t.Fatalf("Cashflow B: %v", err)
		}
		// Each org sees only its own money in — never the other's.
		if resA.Incoming != "500.00" {
			t.Errorf("org A incoming = %q, want 500.00 (leaked org B?)", resA.Incoming)
		}
		if resB.Incoming != "700.00" {
			t.Errorf("org B incoming = %q, want 700.00 (leaked org A?)", resB.Incoming)
		}
	})

	t.Run("non-member is rejected", func(t *testing.T) {
		t.Parallel()
		org, _ := newOrgWithOwner(t, ts)
		// A random user who is NOT a member of the org → authorisation fails.
		if _, err := svc.Cashflow(ctx, uuid.New(), mustUUID(t, org)); err == nil {
			t.Fatal("expected an error for a non-member caller, got nil")
		}
	})
}

// seedContact inserts a minimal contact for an org and returns its id. Its cleanup
// hard-deletes the contact AND every invoice that references it (so the invoices
// seeded onto it go too), before the org row is removed.
func seedContact(t *testing.T, ts *testServer, orgID, userID string) string {
	t.Helper()
	id := uuid.NewString()
	if _, err := ts.pool.Exec(context.Background(), `
		INSERT INTO contacts (id, organisation_id, created_by_user_id, organisation_name)
		VALUES ($1, $2, $3, 'Timeline Test Ltd')`, id, orgID, userID); err != nil {
		t.Fatalf("seedContact: %v", err)
	}
	t.Cleanup(func() {
		ctx := context.Background()
		_, _ = ts.pool.Exec(ctx, `DELETE FROM invoices WHERE contact_id = $1`, id)
		_, _ = ts.pool.Exec(ctx, `DELETE FROM contacts WHERE id = $1`, id)
	})
	return id
}

// seedInvoice inserts one invoice (dated_on = due_on for simplicity) with the given
// status / due date / total / paid. Cleaned up via the contact (seedContact).
func seedInvoice(t *testing.T, ts *testServer, orgID, userID, contactID, status string, due time.Time, totalMinor, paidMinor int64) {
	t.Helper()
	if _, err := ts.pool.Exec(context.Background(), `
		INSERT INTO invoices (organisation_id, created_by_user_id, contact_id, dated_on, due_on, status, total_value_minor, paid_value_minor)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
		orgID, userID, contactID, due, due, status, totalMinor, paidMinor); err != nil {
		t.Fatalf("seedInvoice: %v", err)
	}
}

func TestOverviewInvoiceTimeline(t *testing.T) {
	ts := newTestServer(t)
	t.Cleanup(func() { ts.pool.Close() })
	ctx := context.Background()
	svc := overview.NewService(authdb.New(ts.pool), dboverview.New(ts.pool))

	now := time.Now()
	firstOfThis := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
	lastOfPrev := firstOfThis.AddDate(0, 0, -1)    // always < today → Overdue/Paid bucket in the PREVIOUS month
	prevMonth := firstOfThis.AddDate(0, -1, 0)     // that bucket's key (first-of-month)
	futureMonth := firstOfThis.AddDate(0, 2, 0)    // 1st of month+2 → > today, within +3 window → Due bucket
	const isoDay = "2006-01-02"

	// find returns the bucket whose Month equals the given ISO first-of-month.
	find := func(months []overview.InvoiceTimelineMonth, iso string) *overview.InvoiceTimelineMonth {
		for i := range months {
			if months[i].Month == iso {
				return &months[i]
			}
		}
		return nil
	}

	t.Run("buckets, outstanding and exclusions", func(t *testing.T) {
		t.Parallel()
		org, user := newOrgWithOwner(t, ts)
		contact := seedContact(t, ts, org, user)

		// PREVIOUS month: a fully-paid (→ Paid £1000), an unpaid overdue (→ Overdue
		// £500), and a PART-paid overdue (£200 total, £50 paid → whole £200 Overdue).
		seedInvoice(t, ts, org, user, contact, "SENT", lastOfPrev, 100000, 100000)
		seedInvoice(t, ts, org, user, contact, "SENT", lastOfPrev, 50000, 0)
		seedInvoice(t, ts, org, user, contact, "SENT", lastOfPrev, 20000, 5000)
		// FUTURE month: unpaid, not yet due → Due £300.
		seedInvoice(t, ts, org, user, contact, "SENT", futureMonth, 30000, 0)
		// DRAFT must be excluded everywhere.
		seedInvoice(t, ts, org, user, contact, "DRAFT", lastOfPrev, 99999, 0)

		res, err := svc.InvoiceTimeline(ctx, mustUUID(t, user), mustUUID(t, org))
		if err != nil {
			t.Fatalf("InvoiceTimeline: %v", err)
		}
		if len(res.Months) != 12 {
			t.Fatalf("len(Months) = %d, want 12", len(res.Months))
		}

		prev := find(res.Months, prevMonth.Format(isoDay))
		if prev == nil {
			t.Fatalf("previous month %q not in window", prevMonth.Format(isoDay))
		}
		if prev.Overdue != "700.00" || prev.Paid != "1000.00" || prev.Due != "0.00" {
			t.Errorf("prev month = overdue %q/paid %q/due %q, want 700.00/1000.00/0.00",
				prev.Overdue, prev.Paid, prev.Due)
		}

		fut := find(res.Months, futureMonth.Format(isoDay))
		if fut == nil {
			t.Fatalf("future month %q not in window", futureMonth.Format(isoDay))
		}
		if fut.Due != "300.00" || fut.Overdue != "0.00" || fut.Paid != "0.00" {
			t.Errorf("future month = due %q/overdue %q/paid %q, want 300.00/0.00/0.00",
				fut.Due, fut.Overdue, fut.Paid)
		}

		// Outstanding = unpaid due_value over SENT (DRAFT + the paid one excluded):
		// 500 + (200−50) + 300 = 950.
		if res.Outstanding != "950.00" {
			t.Errorf("outstanding = %q, want 950.00", res.Outstanding)
		}
	})

	t.Run("multi-tenant isolation", func(t *testing.T) {
		t.Parallel()
		orgA, userA := newOrgWithOwner(t, ts)
		contactA := seedContact(t, ts, orgA, userA)
		seedInvoice(t, ts, orgA, userA, contactA, "SENT", lastOfPrev, 40000, 0) // £400 outstanding

		orgB, userB := newOrgWithOwner(t, ts)
		contactB := seedContact(t, ts, orgB, userB)
		seedInvoice(t, ts, orgB, userB, contactB, "SENT", lastOfPrev, 60000, 0) // £600 outstanding

		resA, err := svc.InvoiceTimeline(ctx, mustUUID(t, userA), mustUUID(t, orgA))
		if err != nil {
			t.Fatalf("InvoiceTimeline A: %v", err)
		}
		resB, err := svc.InvoiceTimeline(ctx, mustUUID(t, userB), mustUUID(t, orgB))
		if err != nil {
			t.Fatalf("InvoiceTimeline B: %v", err)
		}
		if resA.Outstanding != "400.00" {
			t.Errorf("org A outstanding = %q, want 400.00 (leaked org B?)", resA.Outstanding)
		}
		if resB.Outstanding != "600.00" {
			t.Errorf("org B outstanding = %q, want 600.00 (leaked org A?)", resB.Outstanding)
		}
	})
}

func TestOverviewBanking(t *testing.T) {
	ts := newTestServer(t)
	t.Cleanup(func() { ts.pool.Close() })
	ctx := context.Background()
	svc := overview.NewService(authdb.New(ts.pool), dboverview.New(ts.pool))

	now := time.Now()
	firstOfThis := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
	preWindow := firstOfThis.AddDate(0, -13, 0) // before the 12-month window start
	twoMonthsAgo := firstOfThis.AddDate(0, -2, 0)
	const isoDay = "2006-01-02"

	find := func(months []overview.BankBalancePoint, iso string) *overview.BankBalancePoint {
		for i := range months {
			if months[i].Month == iso {
				return &months[i]
			}
		}
		return nil
	}

	t.Run("cumulative series, total and account count", func(t *testing.T) {
		t.Parallel()
		org, user := newOrgWithOwner(t, ts)
		// Two accounts: opening £100 + £50 = £150 base.
		acctA := seedAccount(t, ts, org, user, 10000)
		acctB := seedAccount(t, ts, org, user, 5000)

		// Pre-window +£20 → lifts EVERY month-end point (base becomes £170).
		seedTxn(t, ts, org, acctA, preWindow, 2000, false)
		// −£30 two months ago → that month onward drops to £140.
		seedTxn(t, ts, org, acctA, twoMonthsAgo, -3000, false)
		// +£200 this month → only the last point rises to £340.
		seedTxn(t, ts, org, acctB, firstOfThis, 20000, false)
		// Soft-deleted this month → excluded everywhere.
		seedTxn(t, ts, org, acctB, firstOfThis, 99900, true)

		res, err := svc.Banking(ctx, mustUUID(t, user), mustUUID(t, org))
		if err != nil {
			t.Fatalf("Banking: %v", err)
		}
		if len(res.Months) != 12 {
			t.Fatalf("len(Months) = %d, want 12", len(res.Months))
		}
		if res.Accounts != 2 {
			t.Errorf("accounts = %d, want 2", res.Accounts)
		}
		// Current total = opening 150 + (20 − 30 + 200) = 340 (soft-deleted excluded).
		if res.Balance != "340.00" {
			t.Errorf("balance = %q, want 340.00", res.Balance)
		}

		// Oldest point (no in-window txns yet) = base £170; the −£30 month = £140; the
		// last point (current month, +£200) = £340.
		oldest := res.Months[0]
		if oldest.Balance != "170.00" {
			t.Errorf("oldest month = %q, want 170.00 (pre-window txn missing?)", oldest.Balance)
		}
		if m := find(res.Months, twoMonthsAgo.Format(isoDay)); m == nil || m.Balance != "140.00" {
			t.Errorf("two-months-ago = %v, want 140.00", m)
		}
		last := res.Months[11]
		if last.Month != firstOfThis.Format(isoDay) || last.Balance != "340.00" {
			t.Errorf("last month = %s/%s, want %s/340.00", last.Month, last.Balance, firstOfThis.Format(isoDay))
		}
	})

	t.Run("multi-tenant isolation", func(t *testing.T) {
		t.Parallel()
		orgA, userA := newOrgWithOwner(t, ts)
		acctA := seedAccount(t, ts, orgA, userA, 10000) // £100 opening
		seedTxn(t, ts, orgA, acctA, firstOfThis, 5000, false)

		orgB, userB := newOrgWithOwner(t, ts)
		_ = seedAccount(t, ts, orgB, userB, 70000) // £700 opening, no txns

		resA, err := svc.Banking(ctx, mustUUID(t, userA), mustUUID(t, orgA))
		if err != nil {
			t.Fatalf("Banking A: %v", err)
		}
		resB, err := svc.Banking(ctx, mustUUID(t, userB), mustUUID(t, orgB))
		if err != nil {
			t.Fatalf("Banking B: %v", err)
		}
		if resA.Balance != "150.00" { // 100 + 50
			t.Errorf("org A balance = %q, want 150.00 (leaked org B?)", resA.Balance)
		}
		if resB.Balance != "700.00" {
			t.Errorf("org B balance = %q, want 700.00 (leaked org A?)", resB.Balance)
		}
	})
}
