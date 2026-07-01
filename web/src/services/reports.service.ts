import { apiFetch } from '@/lib/api'
import {
  GetTrialBalanceResponseSchema,
  ListAccountsResponseSchema,
  GetAccountTransactionsResponseSchema,
  type TrialBalance,
  type AccountSummary,
  type AccountTransactions,
} from '@/types/report'

// GET /api/v1/reports/trial-balance — the trial balance as of a date (defaults to
// today when no date is passed; iteration 1 is a today snapshot). The bearer token
// is attached by apiFetch, and a 401 is handled there (logout + redirect).
export async function getTrialBalance(date?: string): Promise<TrialBalance> {
  const qs = date ? `?date=${encodeURIComponent(date)}` : ''
  const data = await apiFetch<unknown>(`/reports/trial-balance${qs}`, { method: 'GET' })
  return GetTrialBalanceResponseSchema.parse(data).trial_balance
}

// GET /api/v1/reports/accounts — the org's active Chart-of-Accounts accounts, used
// to populate the Account Transactions report's account picker. An empty list may
// arrive as null, so default to [].
export async function listReportAccounts(): Promise<AccountSummary[]> {
  const data = await apiFetch<unknown>('/reports/accounts', { method: 'GET' })
  return ListAccountsResponseSchema.parse(data).accounts ?? []
}

// GET /api/v1/reports/account-transactions — the general-ledger lines for one
// account (by nominal code) over an optional date range. `from`/`to` are
// YYYY-MM-DD; omit `from` for an open lower bound (the default "All time").
// By default superseded/reversed entries are hidden (only the live entries show);
// pass includeSuperseded to reveal the full reversal chain for auditing.
export async function getAccountTransactions(
  account: string,
  from?: string,
  to?: string,
  includeSuperseded = false,
): Promise<AccountTransactions> {
  const q = new URLSearchParams({ account })
  if (from) q.set('from', from)
  if (to) q.set('to', to)
  if (includeSuperseded) q.set('include_superseded', 'true')
  const data = await apiFetch<unknown>(`/reports/account-transactions?${q.toString()}`, {
    method: 'GET',
  })
  return GetAccountTransactionsResponseSchema.parse(data).account_transactions
}
