import { apiFetch } from '@/lib/api'
import {
  ListBankAccountsResponseSchema,
  GetBankAccountResponseSchema,
  BankAccountTransactionsResponseSchema,
  type BankAccount,
  type BankTransaction,
  type CreateBankAccountRequest,
} from '@/types/bank-account'

// GET /api/v1/bank-accounts — every account in the caller's organisation, each
// with its derived current balance. The bearer token is attached by apiFetch, and
// a 401 is handled there (logout + redirect). An empty list may arrive as null, so
// default to [].
export async function listBankAccounts(): Promise<BankAccount[]> {
  const data = await apiFetch<unknown>('/bank-accounts', { method: 'GET' })
  return ListBankAccountsResponseSchema.parse(data).bank_accounts ?? []
}

// GET /api/v1/bank-accounts/:id — one account, used to pre-fill the edit form. A
// 404 (unknown id / other org) surfaces as an ApiError for the caller to show.
export async function getBankAccount(id: string): Promise<BankAccount> {
  const data = await apiFetch<unknown>(`/bank-accounts/${encodeURIComponent(id)}`, { method: 'GET' })
  return GetBankAccountResponseSchema.parse(data).bank_account
}

// POST /api/v1/bank-accounts — create (owner/admin). A 403 (not owner/admin) or
// 422 (e.g. bad opening_balance) is thrown as an ApiError for the form to display.
export async function createBankAccount(payload: CreateBankAccountRequest): Promise<BankAccount> {
  const data = await apiFetch<unknown>('/bank-accounts', { method: 'POST', body: payload })
  return GetBankAccountResponseSchema.parse(data).bank_account
}

// PUT /api/v1/bank-accounts/:id — full update (owner/admin). Same payload as
// create; returns the updated account (with the re-derived current balance).
export async function updateBankAccount(id: string, payload: CreateBankAccountRequest): Promise<BankAccount> {
  const data = await apiFetch<unknown>(`/bank-accounts/${encodeURIComponent(id)}`, {
    method: 'PUT',
    body: payload,
  })
  return GetBankAccountResponseSchema.parse(data).bank_account
}

// GET /api/v1/bank-accounts/:id/transactions — the read-only statement: the account
// (header/sidebar/opening balance) plus its lines, oldest first, each with a running
// balance computed server-side. The transactions list may arrive as null → default [].
export async function getBankAccountTransactions(
  id: string,
): Promise<{ account: BankAccount; transactions: BankTransaction[] }> {
  const data = await apiFetch<unknown>(`/bank-accounts/${encodeURIComponent(id)}/transactions`, { method: 'GET' })
  const parsed = BankAccountTransactionsResponseSchema.parse(data)
  return { account: parsed.account, transactions: parsed.transactions ?? [] }
}
