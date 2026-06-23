import { z } from 'zod'

// Mirrors the backend's BankAccountResponse (internal/banking/service.go). A bank
// account is one of the ORGANISATION's own accounts. Money is exposed as decimal
// POUND strings (opening_balance, current_balance) — never floats; current_balance
// is derived server-side (opening + Σ live transactions).
export const BankAccountSchema = z.object({
  id: z.string(),
  organisation_id: z.string(),
  created_by_user_id: z.string(),

  name: z.string(),
  currency: z.string(), // ISO 4217, e.g. 'GBP'
  status: z.string(), // 'active' | 'closed'
  is_personal: z.boolean(),
  is_primary: z.boolean(),

  // Account identifiers: UK (sort code), US (routing number), international (IBAN/BIC).
  bank_name: z.string().nullish(),
  account_number: z.string().nullish(),
  sort_code: z.string().nullish(),
  routing_number: z.string().nullish(),
  bank_account_type: z.string().nullish(),
  iban: z.string().nullish(),
  bic: z.string().nullish(),
  show_on_invoices: z.boolean(),

  // Money as pound strings; opening_balance_date is informational.
  opening_balance: z.string(),
  current_balance: z.string(), // derived (opening + Σ transactions)
  opening_balance_date: z.string().nullish(),
  // False once the account has transactions — the opening balance is the
  // running-balance seed, so the form locks it after the first line.
  opening_balance_editable: z.boolean(),

  guess_explanations: z.boolean(),
  created_at: z.string(),
  updated_at: z.string(),
})
export type BankAccount = z.infer<typeof BankAccountSchema>

// GET /api/v1/bank-accounts → { "bank_accounts": [...] | null }. An empty list can
// arrive as null (Go marshals a nil slice to null), so allow null + default to [].
export const ListBankAccountsResponseSchema = z.object({
  bank_accounts: z.array(BankAccountSchema).nullish(),
})

// GET/:id, POST, PUT/:id all return { "bank_account": {...} }.
export const GetBankAccountResponseSchema = z.object({
  bank_account: BankAccountSchema,
})

// POST/PUT body. Mirrors the backend's CreateBankAccountRequest. Optional strings
// are omitted when blank; the two checkboxes that default ON (show_on_invoices,
// guess_explanations) are always sent. opening_balance is a pound string. Reused
// for both create and the full-replace PUT.
export interface CreateBankAccountRequest {
  name: string
  currency?: string
  status?: string
  is_personal?: boolean
  is_primary?: boolean
  bank_name?: string
  account_number?: string
  sort_code?: string
  routing_number?: string
  bank_account_type?: string
  iban?: string
  bic?: string
  show_on_invoices?: boolean
  opening_balance?: string
  opening_balance_date?: string
  guess_explanations?: boolean
}

// One statement line (mirrors the backend's BankTransactionResponse). The backend
// pre-splits money into money_in / money_out (pound strings, exactly one set) by the
// sign of the amount, and running_balance is the derived balance AFTER this line.
export const BankTransactionSchema = z.object({
  id: z.string(),
  dated_on: z.string(), // YYYY-MM-DD
  description: z.string().nullish(),
  bank_memo: z.string().nullish(),
  status: z.string(), // 'unexplained' | 'explained' | 'for_approval'
  source: z.string(), // 'feed' | 'manual' | 'statement'
  is_manual: z.boolean(), // derived from source; gates the edit affordance
  transaction_type: z.string().nullish(), // OFX type (CREDIT/DEBIT/…); reconcile UI later
  money_in: z.string().nullish(),
  money_out: z.string().nullish(),
  unexplained_amount: z.string(), // signed; reconcile UI later
  running_balance: z.string(),
  explanation_summary: z.string().nullish(), // searchable digest of the line's explanations
})
export type BankTransaction = z.infer<typeof BankTransactionSchema>

// POST/PUT body for a MANUAL transaction. Amount is a positive pound string + a
// direction (the backend signs it). Reused for create and edit.
export interface CreateBankTransactionRequest {
  dated_on: string // YYYY-MM-DD
  description?: string
  direction: 'in' | 'out'
  amount: string // pounds, positive
  bank_memo?: string
}

// GET /api/v1/bank-accounts/:id/transactions → { account, transactions } — the
// account (header/sidebar/opening balance) plus its lines, oldest first.
export const BankAccountTransactionsResponseSchema = z.object({
  account: BankAccountSchema,
  transactions: z.array(BankTransactionSchema).nullish(),
})
export type BankAccountTransactions = z.infer<typeof BankAccountTransactionsResponseSchema>

// POST /api/v1/bank-accounts/:id/transactions/import → the import counts plus the
// refreshed statement (a superset of BankAccountTransactionsResponse).
export const StatementImportResultSchema = z.object({
  imported: z.number(),
  skipped_duplicates: z.number(),
  total: z.number(),
  account: BankAccountSchema,
  transactions: z.array(BankTransactionSchema).nullish(),
})
export type StatementImportResult = z.infer<typeof StatementImportResultSchema>

