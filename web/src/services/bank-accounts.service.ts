import { apiFetch, apiUpload } from '@/lib/api'
import {
  ListBankAccountsResponseSchema,
  GetBankAccountResponseSchema,
  BankAccountTransactionsResponseSchema,
  StatementImportResultSchema,
  type BankAccount,
  type BankTransaction,
  type CreateBankAccountRequest,
  type CreateBankTransactionRequest,
  type StatementImportResult,
} from '@/types/bank-account'

// Statement payload shared by the read + the three transaction mutations (all return
// the refreshed { account, transactions }). An empty list may arrive as null → [].
type Statement = { account: BankAccount; transactions: BankTransaction[] }
function parseStatement(data: unknown): Statement {
  const parsed = BankAccountTransactionsResponseSchema.parse(data)
  return { account: parsed.account, transactions: parsed.transactions ?? [] }
}

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
export async function getBankAccountTransactions(id: string): Promise<Statement> {
  const data = await apiFetch<unknown>(`/bank-accounts/${encodeURIComponent(id)}/transactions`, { method: 'GET' })
  return parseStatement(data)
}

// POST /api/v1/bank-accounts/:id/transactions — add a manual transaction (owner/admin).
// Returns the refreshed statement.
export async function createBankTransaction(accountId: string, payload: CreateBankTransactionRequest): Promise<Statement> {
  const data = await apiFetch<unknown>(`/bank-accounts/${encodeURIComponent(accountId)}/transactions`, {
    method: 'POST',
    body: payload,
  })
  return parseStatement(data)
}

// PUT /api/v1/bank-accounts/:id/transactions/:txnId — edit a manual transaction (owner/admin).
export async function updateBankTransaction(
  accountId: string,
  txnId: string,
  payload: CreateBankTransactionRequest,
): Promise<Statement> {
  const data = await apiFetch<unknown>(
    `/bank-accounts/${encodeURIComponent(accountId)}/transactions/${encodeURIComponent(txnId)}`,
    { method: 'PUT', body: payload },
  )
  return parseStatement(data)
}

// DELETE /api/v1/bank-accounts/:id/transactions/:txnId — remove a manual transaction (owner/admin).
export async function deleteBankTransaction(accountId: string, txnId: string): Promise<Statement> {
  const data = await apiFetch<unknown>(
    `/bank-accounts/${encodeURIComponent(accountId)}/transactions/${encodeURIComponent(txnId)}`,
    { method: 'DELETE' },
  )
  return parseStatement(data)
}

// DELETE /api/v1/bank-accounts/:id — soft-delete the account (owner/admin). Returns 204
// (no body); apiFetch tolerates the empty body, so we just await success.
export async function deleteBankAccount(id: string): Promise<void> {
  await apiFetch<unknown>(`/bank-accounts/${encodeURIComponent(id)}`, { method: 'DELETE' })
}

// POST /api/v1/bank-accounts/:id/transactions/import — upload a CSV statement (owner/admin,
// multipart "file"). Returns the import counts + the refreshed statement. A 422 (bad CSV — the
// backend names the offending rows) surfaces as an ApiError for the view to show.
export async function importBankStatement(accountId: string, file: File): Promise<StatementImportResult> {
  const form = new FormData()
  form.append('file', file)
  const data = await apiUpload<unknown>(`/bank-accounts/${encodeURIComponent(accountId)}/transactions/import`, form)
  return StatementImportResultSchema.parse(data)
}
