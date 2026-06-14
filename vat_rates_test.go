package main

// vat_rates_test.go
// =============================================================================
// Integration test (real PostgreSQL) for the ListVatRatesByCountry query added
// alongside the new vat_rates.country_code and vat_rates.is_fixed_ratio columns.
//
// There is no HTTP endpoint for VAT rates yet — exposing one is follow-up work
// (see BACKLOG.md) — so unlike the handler tests in server_test.go this test
// calls the sqlc-generated query directly against the connection pool. It is
// still a real round-trip through PostgreSQL and pgx, which is what the project
// convention asks for: it verifies the SQL itself (date-window filtering and
// country scoping), not just Go wiring.
//
// It reuses newTestServer (server_test.go) to load .env, open the pool, and
// skip cleanly when DATABASE_URL is not set.
//
// Requires the GB seed to be present:
//   psql "$DATABASE_URL" -f db/seeds/vat_rates.sql
// =============================================================================

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"

	expenses "github.com/operationfb/accounting-saas/db/expenses"
)

func TestListVatRatesByCountry(t *testing.T) {
	ts := newTestServer(t)
	defer ts.pool.Close()

	ctx := context.Background()
	// The query lives in the expenses package (vat_rates is defined in
	// schema.sql). expenses.New wraps the pool so we can call it directly.
	q := expenses.New(ts.pool)

	// -------------------------------------------------------------------------
	// Happy path: GB returns the seeded rates that are valid TODAY, with the
	// right is_fixed_ratio values, and EXCLUDES the expired temporary rate.
	// -------------------------------------------------------------------------
	t.Run("GB returns only rates valid today, with correct is_fixed_ratio", func(t *testing.T) {
		rows, err := q.ListVatRatesByCountry(ctx, "GB")
		if err != nil {
			t.Fatalf("ListVatRatesByCountry(GB): %v", err)
		}
		if len(rows) == 0 {
			t.Fatal(`no GB vat_rates found — run the seed: psql "$DATABASE_URL" -f db/seeds/vat_rates.sql`)
		}

		byName := make(map[string]expenses.ListVatRatesByCountryRow, len(rows))
		for _, r := range rows {
			// Every row the query returns must be for the requested country.
			if r.CountryCode != "GB" {
				t.Errorf("row %q has country_code %q, want GB", r.Name, r.CountryCode)
			}
			byName[r.Name] = r
		}

		// A currently-valid fixed-ratio rate: amount is locked to gross × rate.
		std, ok := byName["Standard Rate"]
		if !ok {
			t.Fatal("expected 'Standard Rate' in GB results")
		}
		if std.RateBps != 2000 {
			t.Errorf("Standard Rate rate_bps = %d, want 2000", std.RateBps)
		}
		if !std.IsFixedRatio {
			t.Error("Standard Rate should be fixed-ratio (is_fixed_ratio = true)")
		}

		// The non-fixed-ratio seed row exercises the FALSE branch — the user may
		// enter a custom VAT amount for this rate.
		manual, ok := byName["Standard Rate (manual)"]
		if !ok {
			t.Fatal("expected 'Standard Rate (manual)' in GB results")
		}
		if manual.IsFixedRatio {
			t.Error("'Standard Rate (manual)' should NOT be fixed-ratio (is_fixed_ratio = false)")
		}

		// The temporary COVID hospitality rate has an effective_to in the past,
		// so the date-window filter must EXCLUDE it from today's results.
		if _, found := byName["Hospitality (temporary)"]; found {
			t.Error("expired 'Hospitality (temporary)' rate must not be returned (effective_to is in the past)")
		}
	})

	// -------------------------------------------------------------------------
	// Country scoping: a rate for one country must never appear under another.
	// We insert one ephemeral German rate (cleaned up on exit) and assert it
	// shows up under DE but never under GB — the country-equivalent of the
	// multi-tenant isolation the other tests check for organisations.
	// -------------------------------------------------------------------------
	t.Run("scoped by country — a DE rate never appears under GB", func(t *testing.T) {
		deID := uuid.New()
		if _, err := ts.pool.Exec(ctx,
			`INSERT INTO vat_rates (id, country_code, name, rate_bps, is_fixed_ratio, effective_from)
			 VALUES ($1, 'DE', 'Germany Standard', 1900, TRUE, '2007-01-01')`, deID); err != nil {
			t.Fatalf("insert DE rate: %v", err)
		}
		t.Cleanup(func() { _, _ = ts.pool.Exec(ctx, `DELETE FROM vat_rates WHERE id = $1`, deID) })

		// DE query returns the German rate, and only DE-coded rows.
		deRows, err := q.ListVatRatesByCountry(ctx, "DE")
		if err != nil {
			t.Fatalf("ListVatRatesByCountry(DE): %v", err)
		}
		var foundDE bool
		for _, r := range deRows {
			if r.CountryCode != "DE" {
				t.Errorf("DE query returned a %q row — country scoping is broken", r.CountryCode)
			}
			if r.ID == deID {
				foundDE = true
			}
		}
		if !foundDE {
			t.Error("DE query should return the inserted German rate")
		}

		// GB query must NOT contain the German rate.
		gbRows, err := q.ListVatRatesByCountry(ctx, "GB")
		if err != nil {
			t.Fatalf("ListVatRatesByCountry(GB): %v", err)
		}
		for _, r := range gbRows {
			if r.ID == deID {
				t.Error("GB query leaked a DE rate — country scoping is broken")
			}
		}
	})
}

// TestHandleListVATRates covers GET /api/v1/vat-rates end-to-end through the Gin
// router: the dev org is 'GB', so an authenticated request returns the seeded GB
// rates valid today (with rate_bps, the "20%" display form, and is_fixed_ratio),
// excludes the expired temporary rate, and requires a valid login.
func TestHandleListVATRates(t *testing.T) {
	ts := newTestServer(t)
	defer ts.pool.Close()

	t.Run("authenticated lists the caller org's country rates", func(t *testing.T) {
		recorder := httptest.NewRecorder()
		req, _ := http.NewRequest(http.MethodGet, "/api/v1/vat-rates", nil)
		req.Header.Set("Authorization", bearer(t, ts, devUserID, devOrgID))
		ts.server.router.ServeHTTP(recorder, req)

		if recorder.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d — body: %s", recorder.Code, recorder.Body.String())
		}

		var resp struct {
			VATRates []VATRateResponse `json:"vat_rates"`
		}
		if err := json.Unmarshal(recorder.Body.Bytes(), &resp); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if len(resp.VATRates) == 0 {
			t.Fatal(`expected GB vat_rates — run the seed: psql "$DATABASE_URL" -f db/seeds/vat_rates.sql`)
		}

		byName := make(map[string]VATRateResponse, len(resp.VATRates))
		for _, r := range resp.VATRates {
			byName[r.Name] = r
			if r.ID == "" {
				t.Errorf("rate %q missing id", r.Name)
			}
		}

		// Standard Rate: check the canonical bps, the display form, and the flag.
		std, ok := byName["Standard Rate"]
		if !ok {
			t.Fatal("expected 'Standard Rate' in the list")
		}
		if std.RateBps != 2000 {
			t.Errorf("Standard Rate rate_bps = %d, want 2000", std.RateBps)
		}
		if std.Rate != "20%" {
			t.Errorf("Standard Rate rate = %q, want %q", std.Rate, "20%")
		}
		if !std.IsFixedRatio {
			t.Error("Standard Rate should be fixed-ratio")
		}

		// The non-fixed-ratio rate exposes the custom-amount branch to the client.
		if manual, ok := byName["Standard Rate (manual)"]; !ok {
			t.Error("expected 'Standard Rate (manual)' in the list")
		} else if manual.IsFixedRatio {
			t.Error("'Standard Rate (manual)' should not be fixed-ratio")
		}

		// The expired temporary rate must be filtered out by the date window.
		if _, found := byName["Hospitality (temporary)"]; found {
			t.Error("expired 'Hospitality (temporary)' rate must not be returned")
		}
	})

	t.Run("requires auth", func(t *testing.T) {
		recorder := httptest.NewRecorder()
		req, _ := http.NewRequest(http.MethodGet, "/api/v1/vat-rates", nil)
		ts.server.router.ServeHTTP(recorder, req)
		if recorder.Code != http.StatusUnauthorized {
			t.Errorf("expected 401, got %d — body: %s", recorder.Code, recorder.Body.String())
		}
	})
}
