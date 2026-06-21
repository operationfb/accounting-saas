package currencies

// service_test.go
// =============================================================================
// Integration tests for the currencies service. Like the rest of the repo these
// hit a REAL PostgreSQL (no mocks): they read the seeded currencies table and the
// FK constraints that were applied to the existing currency columns.
//
// They SKIP (not fail) when DATABASE_URL is unavailable, mirroring the repo-wide
// convention so `go test ./...` still passes on a machine without the dev DB.
//
// Prereq: db/seeds/currencies.sql applied and the four currency FKs added (see the
// "Apply to the shared dev DB" steps in the plan).
// =============================================================================

import (
	"context"
	"os"
	"sort"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/joho/godotenv"

	currenciesdb "github.com/operationfb/accounting-saas/db/currencies"
)

// testPool is the shared pool for this package's tests. Opened once in TestMain;
// nil when no DB is reachable (each test then skips via requirePool).
var testPool *pgxpool.Pool

func TestMain(m *testing.M) {
	// Tests run with cwd = this package dir, so the repo-root .env is two levels
	// up. Try a local .env too. Errors are ignored — the vars may already be set,
	// or there may be no DB at all (we skip in that case).
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

// requirePool skips the test when no database is reachable.
func requirePool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	if testPool == nil {
		t.Skip("DATABASE_URL not set / DB unreachable — skipping currencies integration test")
	}
	return testPool
}

// TestListCurrencies covers the happy path plus the two correctness details worth
// pinning: the 0-minor-unit case (JPY) and a NULL symbol surfacing as nil.
func TestListCurrencies(t *testing.T) {
	pool := requirePool(t)
	svc := NewService(currenciesdb.New(pool))

	list, err := svc.ListCurrencies(context.Background())
	if err != nil {
		t.Fatalf("ListCurrencies returned error: %v", err)
	}
	if len(list) == 0 {
		t.Fatal("ListCurrencies returned no rows — was db/seeds/currencies.sql applied?")
	}

	// The query orders by code; verify the slice came back sorted.
	if !sort.SliceIsSorted(list, func(i, j int) bool { return list[i].Code < list[j].Code }) {
		t.Error("currencies are not ordered by code")
	}

	byCode := make(map[string]CurrencyResponse, len(list))
	for _, c := range list {
		byCode[c.Code] = c
	}

	// GBP — name, symbol and the standard 2 minor digits.
	gbp, ok := byCode["GBP"]
	if !ok {
		t.Fatal("GBP missing from the currencies list")
	}
	if gbp.Name != "British Pound" {
		t.Errorf("GBP name = %q, want %q", gbp.Name, "British Pound")
	}
	if gbp.MinorUnit != 2 {
		t.Errorf("GBP minor_unit = %d, want 2", gbp.MinorUnit)
	}
	if gbp.Symbol == nil || *gbp.Symbol != "£" {
		t.Errorf("GBP symbol = %v, want £", gbp.Symbol)
	}

	// JPY — a real 0-minor-unit currency (the whole reason we store minor_unit).
	jpy, ok := byCode["JPY"]
	if !ok {
		t.Fatal("JPY missing from the currencies list")
	}
	if jpy.MinorUnit != 0 {
		t.Errorf("JPY minor_unit = %d, want 0", jpy.MinorUnit)
	}

	// UZS was seeded with a NULL symbol → the response must be nil, not "".
	uzs, ok := byCode["UZS"]
	if !ok {
		t.Fatal("UZS missing from the currencies list")
	}
	if uzs.Symbol != nil {
		t.Errorf("UZS symbol = %q, want nil (seeded NULL)", *uzs.Symbol)
	}
}

// TestGetCurrencyByCodeIsCaseInsensitive pins the upper() normalisation: a
// lower-case code still resolves, which is what makes the query safe to use for
// validating user-submitted input.
func TestGetCurrencyByCodeIsCaseInsensitive(t *testing.T) {
	pool := requirePool(t)
	q := currenciesdb.New(pool)

	row, err := q.GetCurrencyByCode(context.Background(), "gbp")
	if err != nil {
		t.Fatalf(`GetCurrencyByCode("gbp") error: %v`, err)
	}
	if row.Code != "GBP" {
		t.Errorf(`GetCurrencyByCode("gbp").Code = %q, want GBP`, row.Code)
	}
}

// TestCurrencyForeignKeysExist asserts (via a non-mutating catalog read) that the
// four currency columns now reference currencies(code) — documenting the integrity
// layer without writing to the shared dev data.
func TestCurrencyForeignKeysExist(t *testing.T) {
	pool := requirePool(t)

	want := []string{
		"expenses_currency_fkey",
		"expenses_native_currency_fkey",
		"organisations_native_currency_fkey",
		"projects_currency_fkey",
	}

	rows, err := pool.Query(context.Background(), `
		SELECT tc.constraint_name
		FROM information_schema.table_constraints tc
		JOIN information_schema.constraint_column_usage ccu
		  ON tc.constraint_name = ccu.constraint_name
		WHERE tc.constraint_type = 'FOREIGN KEY' AND ccu.table_name = 'currencies'`)
	if err != nil {
		t.Fatalf("querying FK constraints: %v", err)
	}
	defer rows.Close()

	found := map[string]bool{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatalf("scan: %v", err)
		}
		found[name] = true
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows err: %v", err)
	}

	for _, name := range want {
		if !found[name] {
			t.Errorf("expected FK constraint %q referencing currencies(code) to exist", name)
		}
	}
}
