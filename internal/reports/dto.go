package reports

// dto.go
// =============================================================================
// The request/response shapes for the reports endpoints. Money crosses this
// boundary as DECIMAL POUND STRINGS (converted from integer pence in the service
// via money.MinorToPounds) — never as a float, never as raw pence.
// =============================================================================

// TrialBalanceRow is one Chart-of-Accounts account in the trial balance. Exactly
// one of Debit / Credit carries the magnitude; the other is "" (an empty cell on
// the page). A zero-balance account that still has journal lines shows "0.00" in
// Debit (and "" in Credit) — the report lists every account "with some transaction".
type TrialBalanceRow struct {
	NominalCode string `json:"nominal_code"` // FreeAgent code, e.g. "001", "750-1"
	Name        string `json:"name"`         // e.g. "Sales", "Trade Debtors"
	AccountType string `json:"account_type"` // CoA section, e.g. INCOME, CURRENT_ASSET
	Debit       string `json:"debit"`        // pound string when balance >= 0, else ""
	Credit      string `json:"credit"`       // pound string when balance < 0, else ""
}

// TrialBalanceResponse is the full report: the rows (ordered by nominal code) plus
// the balancing totals. TotalDebit always equals TotalCredit (every journal entry
// sums to zero, enforced by the DB balance trigger) — the "Trial Balance Check".
type TrialBalanceResponse struct {
	AsOfDate    string            `json:"as_of_date"` // the snapshot date, YYYY-MM-DD
	Currency    string            `json:"currency"`   // the org's base currency, e.g. "GBP"
	Rows        []TrialBalanceRow `json:"rows"`
	TotalDebit  string            `json:"total_debit"`
	TotalCredit string            `json:"total_credit"`
}

// AccountSummary is one Chart-of-Accounts account, the shape that backs the Account
// Transactions report's account-picker dropdown.
type AccountSummary struct {
	NominalCode string `json:"nominal_code"`
	Name        string `json:"name"`
	AccountType string `json:"account_type"` // CoA section, used to group the dropdown
}

// AccountTransactionRow is one general-ledger line in the Account Transactions
// report. Exactly one of Debit / Credit carries the magnitude (the other is "").
// SourceType + SourceID let the frontend link the Description to the originating
// document (invoice/expense/bill); SourceID is "" for ad-hoc MANUAL journals.
type AccountTransactionRow struct {
	Date        string `json:"date"`        // entry_date, YYYY-MM-DD
	Description string `json:"description"` // the entry narrative (e.g. "Invoice 002")
	SourceType  string `json:"source_type"` // e.g. INVOICE, EXPENSE, BILL
	SourceID    string `json:"source_id"`   // the originating row id, or "" (MANUAL)
	Debit       string `json:"debit"`       // pound string when balance >= 0, else ""
	Credit      string `json:"credit"`      // pound string when balance < 0, else ""
}

// AccountTransactionsResponse is the full per-account report: the account header,
// the chosen date range (FromDate is "" when the lower bound is open), the lines,
// and the column totals.
type AccountTransactionsResponse struct {
	NominalCode string                  `json:"nominal_code"`
	Name        string                  `json:"name"`
	AccountType string                  `json:"account_type"`
	Currency    string                  `json:"currency"`
	FromDate    string                  `json:"from_date"` // YYYY-MM-DD, or "" when open
	ToDate      string                  `json:"to_date"`   // YYYY-MM-DD
	Rows        []AccountTransactionRow `json:"rows"`
	TotalDebit  string                  `json:"total_debit"`
	TotalCredit string                  `json:"total_credit"`
}
