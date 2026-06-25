package vat

// periods_test.go
// =============================================================================
// Pure unit tests for VAT period generation — no DB, no HTTP. These exercise the
// schedule maths directly: the irregular first period, regular subsequent spans,
// the up-to-today bound, the filing-deadline rule, and the incomplete-settings
// guards.
// =============================================================================

import (
	"testing"
	"time"
)

// d parses a YYYY-MM-DD test date (UTC).
func d(s string) time.Time {
	t, err := time.Parse("2006-01-02", s)
	if err != nil {
		panic(err)
	}
	return t
}

func TestGenerateVATPeriods_Quarterly(t *testing.T) {
	// Mirrors the reference screenshot: registered 01 Mar 26, first return ends
	// 31 May 26, quarterly. "Today" is 24 Jun 26.
	got := generateVATPeriods(d("2026-03-01"), d("2026-05-31"), d("2026-06-24"), "quarterly")

	// Two periods have STARTED: [Mar1–May31] (ended) and [Jun1–Aug31] (in progress).
	// The Sep1–Nov30 period hasn't started (Sep1 > 24 Jun) → excluded.
	if len(got) != 2 {
		t.Fatalf("got %d periods, want 2: %+v", len(got), got)
	}
	if !got[0].Start.Equal(d("2026-03-01")) || !got[0].End.Equal(d("2026-05-31")) {
		t.Errorf("period 1: got %s–%s, want 2026-03-01–2026-05-31",
			got[0].Start.Format("2006-01-02"), got[0].End.Format("2006-01-02"))
	}
	if !got[1].Start.Equal(d("2026-06-01")) || !got[1].End.Equal(d("2026-08-31")) {
		t.Errorf("period 2: got %s–%s, want 2026-06-01–2026-08-31",
			got[1].Start.Format("2006-01-02"), got[1].End.Format("2006-01-02"))
	}
	// Deadlines: 31 May → 7 Jul (matches the screenshot's "File by 07 Jul 26"); 31 Aug → 7 Oct.
	if !got[0].Due.Equal(d("2026-07-07")) {
		t.Errorf("period 1 due: got %s, want 2026-07-07", got[0].Due.Format("2006-01-02"))
	}
	if !got[1].Due.Equal(d("2026-10-07")) {
		t.Errorf("period 2 due: got %s, want 2026-10-07", got[1].Due.Format("2006-01-02"))
	}
}

func TestGenerateVATPeriods_Monthly(t *testing.T) {
	// Monthly from 01 Mar 26 (first return ends 31 Mar 26), today 15 May 26.
	// Periods starting on/before 15 May: Mar, Apr, May → 3.
	got := generateVATPeriods(d("2026-03-01"), d("2026-03-31"), d("2026-05-15"), "monthly")
	if len(got) != 3 {
		t.Fatalf("monthly: got %d periods, want 3: %+v", len(got), got)
	}
	if !got[2].Start.Equal(d("2026-05-01")) || !got[2].End.Equal(d("2026-05-31")) {
		t.Errorf("monthly period 3: got %s–%s, want 2026-05-01–2026-05-31",
			got[2].Start.Format("2006-01-02"), got[2].End.Format("2006-01-02"))
	}
}

func TestGenerateVATPeriods_Annually(t *testing.T) {
	// Annually from 01 Apr 26 (first return ends 31 Mar 27), today 01 Dec 26 → one
	// period; the next (Apr27–Mar28) hasn't started.
	got := generateVATPeriods(d("2026-04-01"), d("2027-03-31"), d("2026-12-01"), "annually")
	if len(got) != 1 || !got[0].Start.Equal(d("2026-04-01")) || !got[0].End.Equal(d("2027-03-31")) {
		t.Fatalf("annually: got %+v, want one period 2026-04-01–2027-03-31", got)
	}
}

func TestGenerateVATPeriods_IncompleteOrInvalid(t *testing.T) {
	if generateVATPeriods(d("2026-03-01"), d("2026-05-31"), d("2026-06-24"), "") != nil {
		t.Error("unknown frequency should yield nil")
	}
	if generateVATPeriods(d("2026-06-01"), d("2026-03-31"), d("2026-06-24"), "quarterly") != nil {
		t.Error("first-return end before the effective date should yield nil")
	}
}

func TestVatDueDate(t *testing.T) {
	// The "7th of the second month after the period-end month" rule, including
	// year rollover.
	cases := map[string]string{
		"2026-05-31": "2026-07-07",
		"2026-06-30": "2026-08-07",
		"2026-11-30": "2027-01-07",
		"2026-12-31": "2027-02-07",
	}
	for end, want := range cases {
		if got := vatDueDate(d(end)).Format("2006-01-02"); got != want {
			t.Errorf("vatDueDate(%s): got %s, want %s", end, got, want)
		}
	}
}
