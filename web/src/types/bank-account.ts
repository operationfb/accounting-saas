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
