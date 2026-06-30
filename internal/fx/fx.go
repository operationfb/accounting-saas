// Package fx holds the pure foreign-exchange arithmetic shared across domains —
// converting an amount between two currencies given their (home-relative) rates, and
// computing the realised gain/loss when cash settles a receivable booked at a different
// rate. It is a LEAF: it imports only the money kernel + shopspring/decimal (no DB, no
// pgtype, no domain package), so it is unit-tested directly like money.
//
// Rate convention (matches exchange_rates + invoices.exchange_rate everywhere else in the
// codebase): a currency's rate is HOME (native) units per 1 unit of that currency. The
// home currency therefore has rate 1 (and is never stored).
package fx

import (
	"github.com/shopspring/decimal"

	"github.com/operationfb/accounting-saas/money"
)

// ConvertVia converts amountMinor — held in the FROM currency's minor units — into the TO
// currency's minor units, using each currency's HOME-relative rate (home units per 1 unit
// of that currency; pass decimal 1 for the home currency itself). fromExp / toExp are the
// two currencies' minor_unit (decimal places).
//
// The cross-rate is rateFrom / rateTo (TO units per 1 FROM unit), so the conversion is
// money.ConvertMinor(amount, fromExp, toExp, rateFrom/rateTo) — i.e. via home. Examples:
//   - bank → home: rateTo = 1 ⇒ amount × rateBank (the home value of the cash).
//   - home → invoice: rateFrom = 1 ⇒ amount × (1/rateInvoice).
//   - same currency (equal rates + exps) is exact.
// A non-positive rateTo (would be a division by zero) yields 0; real rates are CHECK > 0.
func ConvertVia(amountMinor int64, fromExp, toExp int, rateFrom, rateTo decimal.Decimal) int64 {
	if amountMinor == 0 || rateTo.LessThanOrEqual(decimal.Zero) {
		return 0
	}
	cross := rateFrom.Div(rateTo)
	return money.ConvertMinor(amountMinor, fromExp, toExp, cross)
}

// RealisedGainLoss returns the realised FX gain/loss (in home minor units) crystallised
// when a receipt settles part of a receivable: the home value of the cash actually
// received (base) minus the home value of the receivable relieved at its ORIGINAL booking
// rate (relief). Positive = gain (the cash was worth more than the debtor carried),
// negative = loss. The sign convention lives here, in one named+tested place.
func RealisedGainLoss(base, relief int64) int64 {
	return base - relief
}
