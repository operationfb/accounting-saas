// Package money centralises the conversions between the integer minor units
// (pence) the database stores and the decimal pound strings the API exposes,
// plus the small pieces of money arithmetic (VAT extraction, basis-point
// formatting) that several domain modules share.
//
// Why a package: expenses, projects and the upcoming invoices module all need
// the same conversions. Keeping one tested copy here avoids the drift that crept
// in when each module rolled its own — e.g. some sites truncated pounds→pence
// while others rounded the same value differently. Financial correctness is
// non-negotiable, so the rounding rule lives in exactly one place and is
// unit-tested directly (these are the first pure unit tests in the repo).
//
// Units & types:
//   - Monetary amounts are INTEGER minor units (pence). 4250 == £42.50.
//   - The functions are int64-based. Pence comfortably fits int32 for a single
//     expense (the int32 ceiling is ~£21.4m), but invoice/billing totals can
//     exceed that, so the shared kernel uses int64; callers writing to int32
//     columns cast on the way out (and may use ClampToInt32 as a guard).
//   - Never use float for money. shopspring/decimal does the arbitrary-precision
//     arithmetic; we convert to integers for storage.
package money

import "github.com/shopspring/decimal"

// hundred is the pounds↔pence factor. decimal values are immutable (every
// operation returns a new value), so sharing one instance across calls is safe.
var hundred = decimal.NewFromInt(100)

// MinorToPounds renders integer minor units (pence) as a 2dp pound string:
// 4250 → "42.50", 0 → "0.00", -500 → "-5.00". StringFixed(2) always keeps two
// decimal places ("42.50", never "42.5").
func MinorToPounds(minor int64) string {
	return decimal.NewFromInt(minor).Div(hundred).StringFixed(2)
}

// PoundsToMinor parses a decimal pound string into integer minor units (pence).
//
// "42.50" → 4250. The result is rounded HALF-UP (half away from zero) to the
// nearest penny, so an input with sub-penny precision is rounded rather than
// rejected: "42.999" → 4300, "42.005" → 4201. An unparseable string returns an
// error (the caller maps it to a validation error).
//
// This is the single canonical pounds→pence conversion for the whole codebase,
// and the financially sensitive direction, so the rounding rule is explicit and
// documented here rather than re-decided at each call site.
func PoundsToMinor(pounds string) (int64, error) {
	d, err := decimal.NewFromString(pounds)
	if err != nil {
		return 0, err
	}
	// Round(0) on shopspring rounds half away from zero, which is HALF-UP for the
	// non-negative amounts money normally carries.
	return d.Mul(hundred).Round(0).IntPart(), nil
}

// BpsToPercent renders a VAT rate held in basis points as a percentage string:
// 2000 → "20%", 1750 → "17.5%", 0 → "0%". (100 bps == 1%.)
func BpsToPercent(bps int32) string {
	return decimal.NewFromInt(int64(bps)).Div(hundred).String() + "%"
}

// ComputeFixedVAT returns the VAT *contained in* a VAT-INCLUSIVE total for a
// fixed-ratio rate. The entered gross already includes VAT, so we EXTRACT the
// VAT fraction rather than add the rate on top:
//
//	total = net + vat,  vat = net × rate   ⇒   vat = total × rate / (100 + rate)
//
// In basis points: vat = total × rate_bps / (10000 + rate_bps) — the HMRC "VAT
// fraction" (20% → 2000/12000 = 1/6; 5% → 500/10500 = 1/21). The denominator is
// always ≥ 10000, so a 0% rate safely yields 0. Rounded half-up to the penny.
// Example: £120.00 (12000p) incl. 20% → 12000 × 2000 / 12000 = 2000p = £20.00.
func ComputeFixedVAT(grossMinor, rateBps int32) int32 {
	v := decimal.NewFromInt(int64(grossMinor)).
		Mul(decimal.NewFromInt(int64(rateBps))).
		// Denominator is 10000 + rate_bps (NOT 10000): the gross is VAT-inclusive,
		// so we extract the VAT fraction rather than add the rate on top.
		Div(decimal.NewFromInt(int64(10000 + rateBps))).
		Round(0) // whole pence, half away from zero
	return int32(v.IntPart())
}

// ClampToInt32 saturates an int64 into int32 range. Used as a defensive guard
// before writing to an INTEGER (int32) money column: a pathological value is
// pinned to the column's ceiling/floor rather than silently wrapping. A real
// receipt never approaches the ±£21.4m int32 limit.
func ClampToInt32(v int64) int32 {
	const maxI32, minI32 int64 = 1<<31 - 1, -(1 << 31)
	switch {
	case v > maxI32:
		return int32(maxI32)
	case v < minI32:
		return int32(minI32)
	default:
		return int32(v)
	}
}
