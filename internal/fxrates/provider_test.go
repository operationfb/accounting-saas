package fxrates

// provider_test.go
// =============================================================================
// Pure, hermetic unit test for the Frankfurter provider's INVERSION math (the one
// bit of arithmetic in the provider). It serves a canned Frankfurter response from
// an httptest server — no real network, no DB — and asserts each quote is inverted
// from "foreign per 1 home" to "home per 1 foreign" (our storage direction).
// =============================================================================

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/shopspring/decimal"
)

func TestFrankfurterProviderInverts(t *testing.T) {
	// Frankfurter quotes foreign-per-home with base=GBP: 1 GBP = 1.25 USD = 1.16 EUR.
	// We expect home-per-foreign: USD → 1/1.25 = 0.80, EUR → 1/1.16 = 0.862069.
	const body = `{"amount":1.0,"base":"GBP","date":"2026-06-29","rates":{"USD":1.25,"EUR":1.16,"JPY":190.0}}`

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Sanity: the path carries the date and base=GBP.
		if got := r.URL.Query().Get("base"); got != "GBP" {
			t.Errorf("base query = %q, want GBP", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()

	p := NewFrankfurterProvider(srv.URL)
	rates, err := p.FetchRates(context.Background(), "GBP", time.Date(2026, 6, 29, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("FetchRates: %v", err)
	}

	want := map[string]string{
		"USD": "0.8",      // 1 / 1.25
		"EUR": "0.862069", // 1 / 1.16, to 6dp
		"JPY": "0.005263", // 1 / 190
	}
	for code, wantStr := range want {
		got, ok := rates[code]
		if !ok {
			t.Errorf("missing rate for %s", code)
			continue
		}
		// Compare at 6dp (the scale we persist at), so the test pins the stored value.
		if !got.Round(rateScale).Equal(decimal.RequireFromString(wantStr)) {
			t.Errorf("%s: got %s, want %s", code, got.Round(rateScale), wantStr)
		}
	}
}
