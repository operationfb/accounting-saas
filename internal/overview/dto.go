package overview

// dto.go
// =============================================================================
// The HTTP boundary DTOs for the Overview dashboard. Every monetary value
// crosses the wire as a decimal pound STRING (produced by money.MinorToPounds);
// the database stores integer minor units (pence). Never float, never a raw
// pgtype here — the service maps the generated rows onto these shapes.
// =============================================================================

// CashflowMonth is one month's bar group on the Cashflow chart: the month plus
// its money-in / money-out totals. Both are POSITIVE pound strings (outgoing is
// already flipped to a positive magnitude by the query), so the chart draws two
// positive bars.
type CashflowMonth struct {
	Month    string `json:"month"`    // ISO first-of-month, e.g. "2025-07-01"
	Incoming string `json:"incoming"` // pounds, e.g. "1234.50"
	Outgoing string `json:"outgoing"` // pounds, e.g. "980.00"
}

// CashflowResponse backs the Cashflow card: exactly 12 monthly buckets
// (oldest→newest) plus the window totals. Balance = Incoming − Outgoing — the NET
// cashflow over the 12 months (can be negative). This is deliberately distinct
// from the actual bank balance the Banking card shows.
type CashflowResponse struct {
	Months   []CashflowMonth `json:"months"`
	Incoming string          `json:"incoming"` // total money in over the window
	Outgoing string          `json:"outgoing"` // total money out over the window
	Balance  string          `json:"balance"`  // incoming − outgoing (signed)
}

// InvoiceTimelineMonth is one month's stacked bar on the Invoice Timeline: SENT
// invoices' totals split into the three status series. Each invoice's whole total
// lands in exactly one of these (so they don't double-count).
type InvoiceTimelineMonth struct {
	Month   string `json:"month"`   // ISO first-of-month, e.g. "2026-06-01"
	Overdue string `json:"overdue"` // pounds — unpaid & past due
	Due     string `json:"due"`     // pounds — unpaid & not yet due ("Open")
	Paid    string `json:"paid"`    // pounds — fully paid (incl. overpaid)
}

// InvoiceTimelineResponse backs the Invoice Timeline card: the monthly buckets
// (oldest→newest) plus the headline Outstanding figure (total unpaid SENT
// receivables, NOT window-bound).
type InvoiceTimelineResponse struct {
	Months      []InvoiceTimelineMonth `json:"months"`
	Outstanding string                 `json:"outstanding"`
}

// BankBalancePoint is one month-end point on the Banking balance-over-time chart:
// the org's TOTAL bank balance (across all live accounts) at that month end.
type BankBalancePoint struct {
	Month   string `json:"month"`   // ISO first-of-month, e.g. "2026-06-01"
	Balance string `json:"balance"` // pounds, signed (can be negative/overdrawn)
}

// BankingResponse backs the Banking card: the month-end balance series
// (oldest→newest) plus the current total balance and the live-account count.
type BankingResponse struct {
	Months   []BankBalancePoint `json:"months"`
	Balance  string             `json:"balance"`  // current total across all accounts
	Accounts int64              `json:"accounts"` // live-account count ("All accounts")
}
