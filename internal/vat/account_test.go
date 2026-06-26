package vat

// account_test.go
// =============================================================================
// Pure unit tests for the HMRC dashboard date-window helper. No DB, no network —
// the full read paths are covered by the root vat_hmrc_account_test.go.
//
// The regression these lock in: HMRC's liabilities/payments endpoints reject a
// ~365-day range right at the documented 366-day boundary (hmrc/vat-api#556), so
// the DEFAULT window must stay comfortably under it.
// =============================================================================

import (
	"testing"
	"time"
)

func TestHMRCWindow(t *testing.T) {
	today := time.Date(2026, 6, 25, 0, 0, 0, 0, time.UTC)

	spanDays := func(t *testing.T, from, to string) float64 {
		t.Helper()
		ft, err := time.Parse("2006-01-02", from)
		if err != nil {
			t.Fatalf("parse from %q: %v", from, err)
		}
		tt, err := time.Parse("2006-01-02", to)
		if err != nil {
			t.Fatalf("parse to %q: %v", to, err)
		}
		return tt.Sub(ft).Hours() / 24
	}

	t.Run("default window stays clear of HMRC's ~365-day rejection boundary", func(t *testing.T) {
		// 0 = liabilities/payments (end today), 35 = obligations (a month ahead).
		for _, ahead := range []int{0, 35} {
			from, to, err := hmrcWindow("", "", today, ahead)
			if err != nil {
				t.Fatalf("ahead=%d: unexpected error %v", ahead, err)
			}
			if span := spanDays(t, from, to); span > 364 {
				t.Errorf("ahead=%d: span %.0f days is too close to HMRC's boundary (want <= 364)", ahead, span)
			}
		}
	})

	t.Run("liabilities/payments window ends today (not a future to-date)", func(t *testing.T) {
		_, to, err := hmrcWindow("", "", today, 0)
		if err != nil {
			t.Fatalf("unexpected error %v", err)
		}
		if to != "2026-06-25" {
			t.Errorf("to: got %q, want today 2026-06-25", to)
		}
	})

	t.Run("an explicit in-range window passes through unchanged", func(t *testing.T) {
		from, to, err := hmrcWindow("2026-01-01", "2026-03-31", today, 0)
		if err != nil || from != "2026-01-01" || to != "2026-03-31" {
			t.Fatalf("got %q..%q err=%v, want 2026-01-01..2026-03-31", from, to, err)
		}
	})

	t.Run("one-sided, reversed and over-long ranges are rejected", func(t *testing.T) {
		cases := map[string][2]string{
			"only from":     {"2026-01-01", ""},
			"only to":       {"", "2026-01-01"},
			"reversed":      {"2026-03-31", "2026-01-01"},
			"over 366 days": {"2024-01-01", "2026-01-01"},
			"bad format":    {"2026/01/01", "2026-03-31"},
		}
		for name, c := range cases {
			if _, _, err := hmrcWindow(c[0], c[1], today, 0); err == nil {
				t.Errorf("%s: expected rejection for from=%q to=%q", name, c[0], c[1])
			}
		}
	})
}
