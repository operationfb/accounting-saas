package currencies

// service.go
// =============================================================================
// CurrencyService — the read-only business layer over the GLOBAL currencies
// reference table (ISO 4217). There is deliberately no organisation scoping and
// no authorisation here: a currency list is universal, the same for every org.
//
// The table is seeded once (db/seeds/currencies.sql) and never written at
// runtime, so this service only ever reads.
// =============================================================================

import (
	"context"

	currenciesdb "github.com/operationfb/accounting-saas/db/currencies"
	"github.com/operationfb/accounting-saas/internal/kernel"
)

// Service is the read-only currencies service. It depends on the sqlc-generated
// Querier interface (not the concrete *Queries) so tests can substitute a fake
// and the production wiring passes the real pool-backed queries.
type Service struct {
	queries currenciesdb.Querier
}

// NewService builds the Service.
func NewService(queries currenciesdb.Querier) *Service {
	return &Service{queries: queries}
}

// CurrencyResponse is the JSON shape returned to clients. It mirrors the columns
// the UI needs to render a currency picker.
type CurrencyResponse struct {
	Code   string  `json:"code"`            // ISO 4217 three-letter code, e.g. "GBP"
	Name   string  `json:"name"`            // full name, e.g. "British Pound"
	Symbol *string `json:"symbol"`          // e.g. "£"; null when no well-known symbol
	// MinorUnit is the number of decimal digits the currency uses (2 for most,
	// 0 for JPY, 3 for KWD). The money kernel still assumes 2 dp today; this is
	// exposed for the frontend and for future currency-aware conversion.
	MinorUnit int16 `json:"minor_unit"`
}

// ListCurrencies returns every currency, ordered by code. Global reference data,
// so it takes no user/org — any authenticated caller gets the same list.
func (s *Service) ListCurrencies(ctx context.Context) ([]CurrencyResponse, error) {
	rows, err := s.queries.ListCurrencies(ctx)
	if err != nil {
		// A read failure here is unexpected (the table is static), so it is a 500.
		return nil, kernel.ErrInternal(err)
	}

	// Pre-size the slice; make([]T, 0, n) returns [] (not nil) so an empty table
	// still serialises as a JSON array, not null.
	out := make([]CurrencyResponse, 0, len(rows))
	for _, r := range rows {
		out = append(out, CurrencyResponse{
			Code:      r.Code,
			Name:      r.Name,
			Symbol:    kernel.NullTextToPtr(r.Symbol), // pgtype.Text -> *string (NULL -> nil)
			MinorUnit: r.MinorUnit,
		})
	}
	return out, nil
}
