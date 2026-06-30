package fx

import (
	"testing"

	"github.com/shopspring/decimal"
)

func dec(s string) decimal.Decimal {
	d, err := decimal.NewFromString(s)
	if err != nil {
		panic(err)
	}
	return d
}

func TestConvertVia(t *testing.T) {
	one := decimal.NewFromInt(1)

	tests := []struct {
		name                      string
		amount                    int64
		fromExp, toExp            int
		rateFrom, rateTo          decimal.Decimal
		want                      int64
	}{
		// EUR → home(GBP): €100.00 at 0.86 GBP/EUR = £86.00. rateTo (home) = 1.
		{"eur to home", 10000, 2, 2, dec("0.86"), one, 8600},
		// home(GBP) → EUR: £86.00 at 0.86 GBP/EUR ⇒ €100.00. rateFrom (home) = 1.
		{"home to eur", 8600, 2, 2, one, dec("0.86"), 10000},
		// Cross-rate USD → EUR via home: $100 at 0.80 GBP/USD, 0.86 GBP/EUR.
		// 100 × (0.80/0.86) = 93.0232… → £-no, €93.02.
		{"usd to eur cross", 10000, 2, 2, dec("0.80"), dec("0.86"), 9302},
		// Same currency (equal rates + exps) is exact.
		{"same currency", 4250, 2, 2, dec("0.86"), dec("0.86"), 4250},
		// Half-up rounding: 333 × (1/3-ish) — use rates that force a .5 boundary.
		// 100 (1.00) at rateFrom 1.005, rateTo 1 → 100 × 1.005 = 100.5 → 101 (half-up).
		{"half up", 100, 2, 2, dec("1.005"), one, 101},
		// Zero amount → 0.
		{"zero amount", 0, 2, 2, dec("0.86"), one, 0},
		// Non-positive rateTo → 0 (guard).
		{"zero rateTo", 100, 2, 2, dec("0.86"), decimal.Zero, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ConvertVia(tt.amount, tt.fromExp, tt.toExp, tt.rateFrom, tt.rateTo)
			if got != tt.want {
				t.Errorf("ConvertVia(%d, %d, %d, %s, %s) = %d, want %d",
					tt.amount, tt.fromExp, tt.toExp, tt.rateFrom, tt.rateTo, got, tt.want)
			}
		})
	}
}

func TestRealisedGainLoss(t *testing.T) {
	tests := []struct {
		name          string
		base, relief  int64
		want          int64
	}{
		{"gain — cash worth more than debtor", 8700, 8600, 100},
		{"loss — cash worth less than debtor", 8500, 8600, -100},
		{"no movement", 8600, 8600, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := RealisedGainLoss(tt.base, tt.relief); got != tt.want {
				t.Errorf("RealisedGainLoss(%d, %d) = %d, want %d", tt.base, tt.relief, got, tt.want)
			}
		})
	}
}
