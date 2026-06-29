package fxrates

// provider.go
// =============================================================================
// Provider — the SEAM between this module and whatever external service supplies
// exchange rates. The Service depends only on this interface, so the source can be
// swapped (a paid xe.com client, HMRC monthly rates, …) without touching storage,
// the refresh job, or the read API.
//
// The first (and only) implementation is Frankfurter (api.frankfurter.dev), which
// serves the European Central Bank's published reference rates for free, with no
// API key. ECB data is daily (working days), which is exactly the granularity an
// invoice rate needs.
//
// DIRECTION CONTRACT (important): FetchRates returns, for each foreign currency,
// HOME units per 1 unit of that currency — the SAME direction exchange_rates.rate
// and invoices.exchange_rate store, so a fetched value is stored and used verbatim.
// Frankfurter quotes the inverse ("foreign per 1 home"), so this impl INVERTS each
// quote. Money is never float: rates are shopspring/decimal throughout.
// =============================================================================

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/shopspring/decimal"
)

// Provider fetches reference rates from an external source. base is the home/native
// currency (e.g. "GBP"); the returned map is keyed by foreign currency code with the
// value being HOME units per 1 unit of that currency. The base currency itself is
// not included (its rate is implicitly 1).
type Provider interface {
	FetchRates(ctx context.Context, base string, on time.Time) (map[string]decimal.Decimal, error)
}

// frankfurterProvider pulls ECB reference rates from the Frankfurter API.
type frankfurterProvider struct {
	baseURL string
	client  *http.Client
}

// DefaultFrankfurterURL is the public Frankfurter host. Overridable via config so a
// self-hosted instance (or a test stub) can be pointed at instead.
const DefaultFrankfurterURL = "https://api.frankfurter.dev/v1"

// NewFrankfurterProvider builds the ECB-backed provider. baseURL defaults to the
// public host when empty.
func NewFrankfurterProvider(baseURL string) Provider {
	if baseURL == "" {
		baseURL = DefaultFrankfurterURL
	}
	return &frankfurterProvider{
		baseURL: baseURL,
		client:  &http.Client{Timeout: 15 * time.Second},
	}
}

// frankfurterResponse is the JSON shape Frankfurter returns, e.g.
//
//	{"amount":1.0,"base":"GBP","date":"2026-06-29","rates":{"EUR":1.17,"USD":1.27}}
//
// rates[X] is the value of X for `amount` units of base — i.e. "foreign per 1 home".
type frankfurterResponse struct {
	Amount float64            `json:"amount"`
	Base   string             `json:"base"`
	Date   string             `json:"date"`
	Rates  map[string]float64 `json:"rates"`
}

func (p *frankfurterProvider) FetchRates(ctx context.Context, base string, on time.Time) (map[string]decimal.Decimal, error) {
	// Frankfurter path: /<YYYY-MM-DD>?base=GBP  (or /latest for the most recent).
	// Asking for a specific date returns that date's rates (or the nearest prior
	// working day), which is exactly our "on or before" semantics at the source.
	url := fmt.Sprintf("%s/%s?base=%s", p.baseURL, on.Format("2006-01-02"), base)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("fxrates: build request: %w", err)
	}
	resp, err := p.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fxrates: fetch rates: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("fxrates: provider returned HTTP %d", resp.StatusCode)
	}

	var body frankfurterResponse
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return nil, fmt.Errorf("fxrates: decode response: %w", err)
	}

	// Invert each quote (foreign-per-home → home-per-foreign), so the stored rate
	// reads "HOME units per 1 unit of currency", matching invoices.exchange_rate.
	// amount is normally 1, but divide it out defensively in case the API echoes a
	// different amount. shopspring/decimal — no float drift past this boundary.
	out := make(map[string]decimal.Decimal, len(body.Rates))
	amount := decimal.NewFromFloat(body.Amount)
	if amount.IsZero() {
		amount = decimal.NewFromInt(1)
	}
	for code, perHome := range body.Rates {
		quote := decimal.NewFromFloat(perHome)
		if quote.IsZero() {
			continue // skip an unusable zero quote rather than divide by zero
		}
		// home per 1 foreign = amount(base) / (foreign per amount-of-base).
		out[code] = amount.Div(quote)
	}
	return out, nil
}
