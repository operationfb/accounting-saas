package money

import (
	"math"
	"testing"
)

func TestMinorToPounds(t *testing.T) {
	cases := []struct {
		name  string
		minor int64
		want  string
	}{
		{"typical", 4250, "42.50"},
		{"zero", 0, "0.00"},
		{"whole pounds keep 2dp", 4200, "42.00"},
		{"single penny", 1, "0.01"},
		{"negative (amount owed back to employee)", -500, "-5.00"},
		{"beyond int32 range", 3_000_000_000, "30000000.00"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := MinorToPounds(c.minor); got != c.want {
				t.Errorf("MinorToPounds(%d) = %q, want %q", c.minor, got, c.want)
			}
		})
	}
}

func TestPoundsToMinor(t *testing.T) {
	cases := []struct {
		name    string
		pounds  string
		want    int64
		wantErr bool
	}{
		{"typical", "42.50", 4250, false},
		{"whole number", "42", 4200, false},
		{"zero", "0", 0, false},
		// The rounding rule: HALF-UP, accept any precision. The first two cases are
		// exactly the behaviour change vs the old truncating gross conversion.
		{"three dp rounds up", "42.999", 4300, false},
		{"half penny rounds up", "42.005", 4201, false}, // 4200.5 → 4201
		{"below half penny rounds down", "42.004", 4200, false},
		{"negative", "-5.00", -500, false},
		{"unparseable", "abc", 0, true},
		{"empty string", "", 0, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := PoundsToMinor(c.pounds)
			if c.wantErr {
				if err == nil {
					t.Fatalf("PoundsToMinor(%q): expected error, got nil (result %d)", c.pounds, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("PoundsToMinor(%q): unexpected error %v", c.pounds, err)
			}
			if got != c.want {
				t.Errorf("PoundsToMinor(%q) = %d, want %d", c.pounds, got, c.want)
			}
		})
	}
}

// TestRoundTrip checks that a clean 2dp value survives pounds→pence→pounds.
func TestRoundTrip(t *testing.T) {
	for _, s := range []string{"0.00", "0.01", "42.50", "100.00", "1234.99"} {
		minor, err := PoundsToMinor(s)
		if err != nil {
			t.Fatalf("PoundsToMinor(%q): %v", s, err)
		}
		if got := MinorToPounds(minor); got != s {
			t.Errorf("round trip %q → %d → %q", s, minor, got)
		}
	}
}

func TestBpsToPercent(t *testing.T) {
	cases := []struct {
		bps  int32
		want string
	}{
		{2000, "20%"},
		{1750, "17.5%"},
		{500, "5%"},
		{0, "0%"},
	}
	for _, c := range cases {
		t.Run(c.want, func(t *testing.T) {
			if got := BpsToPercent(c.bps); got != c.want {
				t.Errorf("BpsToPercent(%d) = %q, want %q", c.bps, got, c.want)
			}
		})
	}
}

func TestComputeFixedVAT(t *testing.T) {
	cases := []struct {
		name    string
		gross   int32
		rateBps int32
		want    int32
	}{
		{"20% of £120 inclusive (1/6)", 12000, 2000, 2000},
		{"5% of £105 inclusive", 10500, 500, 500},
		{"zero rate yields zero", 5000, 0, 0},
		{"rounds half-up", 999, 2000, 167}, // 999*2000/12000 = 166.5 → 167
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := ComputeFixedVAT(c.gross, c.rateBps); got != c.want {
				t.Errorf("ComputeFixedVAT(%d, %d) = %d, want %d", c.gross, c.rateBps, got, c.want)
			}
		})
	}
}

func TestClampToInt32(t *testing.T) {
	const maxI32 int64 = 1<<31 - 1
	const minI32 int64 = -(1 << 31)
	cases := []struct {
		name string
		in   int64
		want int32
	}{
		{"in range", 4250, 4250},
		{"zero", 0, 0},
		{"at max", maxI32, math.MaxInt32},
		{"over max saturates", maxI32 + 1, math.MaxInt32},
		{"at min", minI32, math.MinInt32},
		{"under min saturates", minI32 - 1, math.MinInt32},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := ClampToInt32(c.in); got != c.want {
				t.Errorf("ClampToInt32(%d) = %d, want %d", c.in, got, c.want)
			}
		})
	}
}
