import { z } from 'zod'

// The Trial Balance report as returned by GET /api/v1/reports/trial-balance.
// Mirrors the Go TrialBalanceResponse (internal/reports/dto.go): money arrives as
// decimal POUND strings (e.g. "2400.00") — never do arithmetic on them, just
// format for display. Exactly one of debit / credit carries a value per row; the
// other is "" (a blank cell). total_debit always equals total_credit (the books
// balance — the "Trial Balance Check").

export const TrialBalanceRowSchema = z.object({
  nominal_code: z.string(), // FreeAgent code, e.g. "001", "750-1"
  name: z.string(),
  account_type: z.string(), // CoA section, e.g. INCOME, CURRENT_ASSET
  debit: z.string(), // pound string, or "" when this row is credit-side
  credit: z.string(), // pound string, or "" when this row is debit-side
})
export type TrialBalanceRow = z.infer<typeof TrialBalanceRowSchema>

export const TrialBalanceSchema = z.object({
  as_of_date: z.string(), // YYYY-MM-DD snapshot date
  currency: z.string(), // org base currency, e.g. "GBP"
  rows: z.array(TrialBalanceRowSchema).nullish(), // null for an empty ledger
  total_debit: z.string(),
  total_credit: z.string(),
})
export type TrialBalance = z.infer<typeof TrialBalanceSchema>

// GET /api/v1/reports/trial-balance → { trial_balance: {...} }.
export const GetTrialBalanceResponseSchema = z.object({
  trial_balance: TrialBalanceSchema,
})

// ---------------------------------------------------------------------------
// Account Transactions report (GET /api/v1/reports/account-transactions) — the
// per-account drill-down reached from the Trial Balance. Mirrors the Go
// AccountTransactionsResponse (internal/reports/dto.go).
// ---------------------------------------------------------------------------

// One Chart-of-Accounts account, backing the account-picker dropdown
// (GET /api/v1/reports/accounts).
export const AccountSummarySchema = z.object({
  nominal_code: z.string(),
  name: z.string(),
  account_type: z.string(), // CoA section, used to group the dropdown
})
export type AccountSummary = z.infer<typeof AccountSummarySchema>

export const ListAccountsResponseSchema = z.object({
  accounts: z.array(AccountSummarySchema).nullish(),
})

// One general-ledger line. source_type + source_id let the UI link the
// Description to the originating document; source_id is "" for MANUAL journals.
export const AccountTransactionRowSchema = z.object({
  date: z.string(), // YYYY-MM-DD
  description: z.string(), // e.g. "Invoice 002"
  source_type: z.string(),
  source_id: z.string(),
  debit: z.string(), // pound string, or "" when credit-side
  credit: z.string(), // pound string, or "" when debit-side
})
export type AccountTransactionRow = z.infer<typeof AccountTransactionRowSchema>

export const AccountTransactionsSchema = z.object({
  nominal_code: z.string(),
  name: z.string(),
  account_type: z.string(),
  currency: z.string(),
  from_date: z.string(), // YYYY-MM-DD, or "" when the lower bound is open
  to_date: z.string(),
  rows: z.array(AccountTransactionRowSchema).nullish(), // null when no lines
  total_debit: z.string(),
  total_credit: z.string(),
})
export type AccountTransactions = z.infer<typeof AccountTransactionsSchema>

export const GetAccountTransactionsResponseSchema = z.object({
  account_transactions: AccountTransactionsSchema,
})
