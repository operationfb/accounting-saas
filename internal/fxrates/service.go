package fxrates

// service.go
// =============================================================================
// Service — the business layer over the GLOBAL exchange_rates reference table.
//
// Two halves:
//   - WRITE (RefreshRates): pull the day's rates from the Provider and UPSERT them.
//     Driven by a daily Cloud Scheduler job (internal endpoint) + a best-effort
//     fetch on startup. Nil-guarded: with no provider configured, refresh is a no-op
//     and the module still SERVES whatever rates are already stored.
//   - READ (RateOnOrBefore / ListOnDate): used by the invoices auto-fill, the read
//     API, and (later) the FX revaluation job.
//
// Like currencies, this is GLOBAL reference data — no organisation scoping, no
// authorisation here (the read HTTP handler still sits behind bearer auth, and the
// refresh behind workflow OIDC). Rates are stored "HOME (GBP) units per 1 unit of
// currency"; the home/base currency is Service config (default GBP) and is never
// stored as a row of its own (its rate is implicitly 1).
// =============================================================================

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/shopspring/decimal"

	currenciesdb "github.com/operationfb/accounting-saas/db/currencies"
	fxratesdb "github.com/operationfb/accounting-saas/db/exchange_rates"
	"github.com/operationfb/accounting-saas/internal/kernel"
)

// rateScale is the number of decimal places we persist a rate to. It matches the
// exchange_rates.rate column type NUMERIC(18,6) — storing more would be silently
// truncated by the DB, so we round explicitly here.
const rateScale = 6

// Service is the exchange-rate service.
type Service struct {
	queries  fxratesdb.Querier
	currency currenciesdb.Querier // the valid ISO code set (FK target); refresh skips unknown codes
	provider Provider             // nil ⇒ refresh is a no-op (reads still work)
	home     string               // home/base currency, e.g. "GBP"
	source   string               // provenance label stored on each row, e.g. "ecb"
}

// NewService builds the Service. provider may be nil (refresh disabled). home
// defaults to "GBP" and source to "ecb" when blank.
func NewService(queries fxratesdb.Querier, currency currenciesdb.Querier, provider Provider, home, source string) *Service {
	if home == "" {
		home = "GBP"
	}
	if source == "" {
		source = "ecb"
	}
	return &Service{queries: queries, currency: currency, provider: provider, home: home, source: source}
}

// HomeCurrency returns the configured home/base currency.
func (s *Service) HomeCurrency() string { return s.home }

// =============================================================================
// WRITE — refresh
// =============================================================================

// RefreshRates fetches the day's rates from the provider and UPSERTs them for every
// currency we know about (skipping the home currency and any code not in the
// currencies table, which the FK would reject). Returns the number of rates stored.
// A nil provider is a no-op (returns 0) so an unconfigured deployment still boots.
func (s *Service) RefreshRates(ctx context.Context, on time.Time) (int, error) {
	if s.provider == nil {
		return 0, nil
	}

	rates, err := s.provider.FetchRates(ctx, s.home, on)
	if err != nil {
		return 0, kernel.ErrInternal(err)
	}

	// Build the set of valid currency codes once, so an upsert can't fail the FK on
	// a code the provider returns that we don't carry.
	known, err := s.knownCurrencies(ctx)
	if err != nil {
		return 0, err
	}

	day := pgtype.Date{Time: on, Valid: true}
	stored := 0
	for code, rate := range rates {
		code = normaliseCode(code)
		if code == s.home || !known[code] || !rate.IsPositive() {
			continue
		}
		if err := s.queries.UpsertRate(ctx, fxratesdb.UpsertRateParams{
			Currency: code,
			RateDate: day,
			Rate:     rate.Round(rateScale).String(),
			Source:   s.source,
		}); err != nil {
			return stored, kernel.ErrInternal(err)
		}
		stored++
	}
	return stored, nil
}

// knownCurrencies returns the set of ISO codes present in the currencies table.
func (s *Service) knownCurrencies(ctx context.Context) (map[string]bool, error) {
	rows, err := s.currency.ListCurrencies(ctx)
	if err != nil {
		return nil, kernel.ErrInternal(err)
	}
	set := make(map[string]bool, len(rows))
	for _, r := range rows {
		set[normaliseCode(r.Code)] = true
	}
	return set, nil
}

// =============================================================================
// READ — lookups
// =============================================================================

// RateOnOrBefore returns the rate (HOME per 1 unit of currency) effective on or
// before `on`. The home currency itself returns 1. The bool is false when we have
// no stored rate for the currency — callers decide whether that's an error (the
// invoice auto-fill turns it into a 422; the read API into a 404).
func (s *Service) RateOnOrBefore(ctx context.Context, currency string, on time.Time) (decimal.Decimal, bool, error) {
	currency = normaliseCode(currency)
	if currency == s.home {
		return decimal.NewFromInt(1), true, nil
	}
	row, err := s.queries.GetRateOnOrBefore(ctx, fxratesdb.GetRateOnOrBeforeParams{
		Currency: currency,
		RateDate: pgtype.Date{Time: on, Valid: true},
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return decimal.Decimal{}, false, nil
		}
		return decimal.Decimal{}, false, kernel.ErrInternal(err)
	}
	rate, derr := decimal.NewFromString(row.Rate)
	if derr != nil {
		return decimal.Decimal{}, false, kernel.ErrInternal(derr)
	}
	return rate, true, nil
}

// RateResponse is the JSON shape the read API returns.
type RateResponse struct {
	Currency string `json:"currency"`  // ISO 4217 foreign code
	Rate     string `json:"rate"`      // HOME units per 1 unit of currency, decimal string
	RateDate string `json:"rate_date"` // YYYY-MM-DD the rate is effective from
	Source   string `json:"source"`    // provenance, e.g. "ecb"
}

// LookupResponse is a single-currency read (GET /exchange-rates/:currency). It adds
// the home currency for context so the SPA knows the direction.
type LookupResponse struct {
	RateResponse
	Base string `json:"base"` // home currency the rate is expressed in
}

// Lookup returns one currency's rate on or before a date, or (nil, nil) when there
// is no stored rate. The handler maps the nil to a 404.
func (s *Service) Lookup(ctx context.Context, currency string, on time.Time) (*LookupResponse, error) {
	currency = normaliseCode(currency)
	if currency == s.home {
		return &LookupResponse{
			RateResponse: RateResponse{Currency: currency, Rate: "1", RateDate: on.Format("2006-01-02"), Source: "home"},
			Base:         s.home,
		}, nil
	}
	row, err := s.queries.GetRateOnOrBefore(ctx, fxratesdb.GetRateOnOrBeforeParams{
		Currency: currency,
		RateDate: pgtype.Date{Time: on, Valid: true},
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, kernel.ErrInternal(err)
	}
	return &LookupResponse{
		RateResponse: RateResponse{
			Currency: row.Currency,
			Rate:     row.Rate,
			RateDate: row.RateDate.Time.Format("2006-01-02"),
			Source:   row.Source,
		},
		Base: s.home,
	}, nil
}

// ListOnDate returns every currency's most recent rate on or before a date (one row
// per currency), for the read API's full table.
func (s *Service) ListOnDate(ctx context.Context, on time.Time) ([]RateResponse, error) {
	rows, err := s.queries.ListRatesOnDate(ctx, pgtype.Date{Time: on, Valid: true})
	if err != nil {
		return nil, kernel.ErrInternal(err)
	}
	out := make([]RateResponse, 0, len(rows))
	for _, r := range rows {
		out = append(out, RateResponse{
			Currency: r.Currency,
			Rate:     r.Rate,
			RateDate: r.RateDate.Time.Format("2006-01-02"),
			Source:   r.Source,
		})
	}
	return out, nil
}

// =============================================================================
// helpers
// =============================================================================

// RefreshTodayBestEffort runs a refresh for today and only logs on failure. Used
// for the startup fetch (and any caller that wants "try, but don't fail the boot").
func (s *Service) RefreshTodayBestEffort(ctx context.Context) {
	n, err := s.RefreshRates(ctx, time.Now())
	if err != nil {
		slog.Warn("fxrates: startup rate refresh failed", "err", err)
		return
	}
	if n > 0 {
		slog.Info("fxrates: refreshed rates", "count", n)
	}
}

// normaliseCode upper-cases and trims an ISO currency code (codes are stored
// uppercase; we no longer upper() in SQL, so normalise here).
func normaliseCode(code string) string {
	return strings.ToUpper(strings.TrimSpace(code))
}
