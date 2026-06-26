package vat

// reconcile_test.go
// =============================================================================
// Pure unit tests for the HMRC period-derivation logic — no DB, no HTTP. They
// exercise inferFrequency (span → frequency) and deriveSettingsFromObligations
// (earliest-obligation anchor), the maths the post-connect reconciliation relies
// on. (The `d` YYYY-MM-DD helper lives in periods_test.go, same package.)
// =============================================================================

import "testing"

func TestInferFrequency(t *testing.T) {
	cases := []struct {
		name       string
		start, end string
		want       string
	}{
		{"quarterly", "2017-01-01", "2017-03-31", "quarterly"},
		{"quarterly Feb-stagger", "2017-02-01", "2017-04-30", "quarterly"},
		{"monthly", "2017-01-01", "2017-01-31", "monthly"},
		{"monthly Feb", "2017-02-01", "2017-02-28", "monthly"},
		{"annually", "2017-01-01", "2017-12-31", "annually"},
		{"odd span (2 months) → unknown", "2017-01-01", "2017-02-28", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := inferFrequency(d(c.start), d(c.end)); got != c.want {
				t.Errorf("inferFrequency(%s, %s) = %q, want %q", c.start, c.end, got, c.want)
			}
		})
	}
}

func TestDeriveSettingsFromObligations(t *testing.T) {
	// Two quarterly obligations, given out of order; the EARLIEST (by start) anchors
	// the schedule. Mirrors the sandbox shape (one open, one fulfilled).
	obs := []hmrcObligation{
		{PeriodKey: "18A2", Start: "2017-04-01", End: "2017-06-30", Due: "2017-08-07", Status: "O"},
		{PeriodKey: "18A1", Start: "2017-01-01", End: "2017-03-31", Due: "2017-05-07", Status: "F"},
	}
	got, ok := deriveSettingsFromObligations(obs)
	if !ok {
		t.Fatal("deriveSettingsFromObligations returned ok=false, want true")
	}
	if !got.EffectiveDate.Equal(d("2017-01-01")) {
		t.Errorf("effective: got %s, want 2017-01-01", got.EffectiveDate.Format("2006-01-02"))
	}
	if !got.FirstReturnPeriodEnd.Equal(d("2017-03-31")) {
		t.Errorf("firstEnd: got %s, want 2017-03-31", got.FirstReturnPeriodEnd.Format("2006-01-02"))
	}
	if got.ReturnFrequency != "quarterly" {
		t.Errorf("frequency: got %q, want quarterly", got.ReturnFrequency)
	}
}

func TestDeriveSettingsFromObligations_Empty(t *testing.T) {
	if _, ok := deriveSettingsFromObligations(nil); ok {
		t.Error("empty obligations: got ok=true, want false")
	}
}

// The derived settings must, when fed back into generateVATPeriods, reproduce a
// schedule whose period-ends include HMRC's obligation ends — that is the whole
// point of the alignment.
func TestDerivedSettingsAlignWithObligations(t *testing.T) {
	obs := []hmrcObligation{
		{Start: "2017-01-01", End: "2017-03-31", Status: "F"},
		{Start: "2017-04-01", End: "2017-06-30", Status: "O"},
	}
	s, ok := deriveSettingsFromObligations(obs)
	if !ok {
		t.Fatal("ok=false")
	}
	ends := periodEndSet(s.EffectiveDate, s.FirstReturnPeriodEnd, d("2017-09-15"), s.ReturnFrequency)
	for _, o := range obs {
		if !ends[o.End] {
			t.Errorf("generated schedule is missing obligation end %s; got set %v", o.End, ends)
		}
	}
}
