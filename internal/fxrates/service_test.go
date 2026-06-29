package fxrates

// service_test.go
// =============================================================================
// Integration tests for the exchange-rate service against a REAL PostgreSQL (the
// repo-wide convention — only external services are faked, here the rate Provider).
// They SKIP when DATABASE_URL is unavailable so `go test ./...` still passes without
// the dev DB.
//
// Each test writes rows for a distinctive far-past rate_date and DELETEs them in
// t.Cleanup, so the shared dev exchange_rates table stays clean.
//
// Prereq: db/schema/fxrates_schema.sql applied + db/seeds/currencies.sql seeded.
// =============================================================================

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/joho/godotenv"
	"github.com/shopspring/decimal"

	currenciesdb "github.com/operationfb/accounting-saas/db/currencies"
	fxratesdb "github.com/operationfb/accounting-saas/db/exchange_rates"
)

var testPool *pgxpool.Pool

func TestMain(m *testing.M) {
	_ = godotenv.Load(".env")
	_ = godotenv.Load("../../.env")

	if dbURL := os.Getenv("DATABASE_URL"); dbURL != "" {
		if pool, err := pgxpool.New(context.Background(), dbURL); err == nil {
			if pool.Ping(context.Background()) == nil {
				testPool = pool
			}
		}
	}

	code := m.Run()
	if testPool != nil {
		testPool.Close()
	}
	os.Exit(code)
}

func requirePool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	if testPool == nil {
		t.Skip("DATABASE_URL not set / DB unreachable — skipping fxrates integration test")
	}
	return testPool
}

// cleanupDate removes any rows this test wrote for a given rate_date, so the shared
// table is left as it was found.
func cleanupDate(t *testing.T, pool *pgxpool.Pool, day time.Time) {
	t.Helper()
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM exchange_rates WHERE rate_date = $1`, day)
	})
}

// fakeProvider is the only mock — it stands in for the external rate source.
type fakeProvider struct {
	rates map[string]decimal.Decimal
	err   error
}

func (f fakeProvider) FetchRates(_ context.Context, _ string, _ time.Time) (map[string]decimal.Decimal, error) {
	return f.rates, f.err
}

// TestRefreshRatesStoresKnownSkipsHomeAndUnknown checks the refresh write path: it
// stores rates for known foreign currencies, skips the home currency, and skips a
// code that isn't in the currencies table (which the FK would otherwise reject).
func TestRefreshRatesStoresKnownSkipsHomeAndUnknown(t *testing.T) {
	pool := requirePool(t)
	day := time.Date(1990, 1, 2, 0, 0, 0, 0, time.UTC)
	cleanupDate(t, pool, day)

	prov := fakeProvider{rates: map[string]decimal.Decimal{
		"EUR":  decimal.RequireFromString("0.86"),
		"USD":  decimal.RequireFromString("0.80"),
		"GBP":  decimal.RequireFromString("1"),   // home — must be skipped
		"ZZZ9": decimal.RequireFromString("0.50"), // not a real currency — must be skipped (no FK)
	}}
	svc := NewService(fxratesdb.New(pool), currenciesdb.New(pool), prov, "GBP", "ecb")

	n, err := svc.RefreshRates(context.Background(), day)
	if err != nil {
		t.Fatalf("RefreshRates: %v", err)
	}
	if n != 2 {
		t.Fatalf("stored %d rates, want 2 (EUR, USD; GBP + ZZZ9 skipped)", n)
	}

	// EUR landed with the right value.
	got, ok, err := svc.RateOnOrBefore(context.Background(), "EUR", day)
	if err != nil || !ok {
		t.Fatalf("RateOnOrBefore(EUR): ok=%v err=%v", ok, err)
	}
	if !got.Equal(decimal.RequireFromString("0.86")) {
		t.Errorf("EUR rate = %s, want 0.86", got)
	}

	// Home currency returns 1 without any stored row.
	home, ok, err := svc.RateOnOrBefore(context.Background(), "GBP", day)
	if err != nil || !ok || !home.Equal(decimal.NewFromInt(1)) {
		t.Errorf("home rate: got %s ok=%v err=%v, want 1", home, ok, err)
	}
}

// TestRefreshRatesNilProviderNoop confirms an unconfigured provider is a no-op (the
// module still boots and serves stored rates).
func TestRefreshRatesNilProviderNoop(t *testing.T) {
	pool := requirePool(t)
	svc := NewService(fxratesdb.New(pool), currenciesdb.New(pool), nil, "GBP", "ecb")
	n, err := svc.RefreshRates(context.Background(), time.Now())
	if err != nil {
		t.Fatalf("RefreshRates(nil provider): %v", err)
	}
	if n != 0 {
		t.Errorf("nil provider stored %d, want 0", n)
	}
}

// TestRateOnOrBeforeFallsBackToPriorDay pins the nearest-prior lookup: with rates on
// D-2 and D, a query for D-1 returns D-2's rate (the missing-day fallback).
func TestRateOnOrBeforeFallsBackToPriorDay(t *testing.T) {
	pool := requirePool(t)
	q := fxratesdb.New(pool)
	svc := NewService(q, currenciesdb.New(pool), nil, "GBP", "ecb")

	d2 := time.Date(1990, 3, 1, 0, 0, 0, 0, time.UTC) // D-2
	d0 := time.Date(1990, 3, 3, 0, 0, 0, 0, time.UTC) // D
	cleanupDate(t, pool, d2)
	cleanupDate(t, pool, d0)

	mustUpsert(t, q, "EUR", d2, "0.80")
	mustUpsert(t, q, "EUR", d0, "0.90")

	// Query D-1 → should return the D-2 rate (0.80), not the future D rate.
	d1 := time.Date(1990, 3, 2, 0, 0, 0, 0, time.UTC)
	got, ok, err := svc.RateOnOrBefore(context.Background(), "EUR", d1)
	if err != nil || !ok {
		t.Fatalf("RateOnOrBefore(D-1): ok=%v err=%v", ok, err)
	}
	if !got.Equal(decimal.RequireFromString("0.80")) {
		t.Errorf("D-1 rate = %s, want 0.80 (the prior day)", got)
	}

	// Query D itself → the D rate.
	got, _, _ = svc.RateOnOrBefore(context.Background(), "EUR", d0)
	if !got.Equal(decimal.RequireFromString("0.90")) {
		t.Errorf("D rate = %s, want 0.90", got)
	}
}

// TestRateOnOrBeforeMissingCurrency confirms a currency with no stored rate reports
// not-found (ok=false), which the invoice auto-fill turns into a clean 422.
func TestRateOnOrBeforeMissingCurrency(t *testing.T) {
	pool := requirePool(t)
	svc := NewService(fxratesdb.New(pool), currenciesdb.New(pool), nil, "GBP", "ecb")
	// A real currency we've stored nothing for, on a date far from any seeded rate.
	_, ok, err := svc.RateOnOrBefore(context.Background(), "EUR", time.Date(1980, 1, 1, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("RateOnOrBefore: %v", err)
	}
	if ok {
		t.Errorf("expected ok=false for a date before any stored EUR rate")
	}
}

func pgDate(t time.Time) pgtype.Date { return pgtype.Date{Time: t, Valid: true} }

func mustUpsert(t *testing.T, q *fxratesdb.Queries, code string, day time.Time, rate string) {
	t.Helper()
	if err := q.UpsertRate(context.Background(), fxratesdb.UpsertRateParams{
		Currency: code,
		RateDate: pgDate(day),
		Rate:     rate,
		Source:   "test",
	}); err != nil {
		t.Fatalf("UpsertRate(%s, %s): %v", code, day.Format("2006-01-02"), err)
	}
}
