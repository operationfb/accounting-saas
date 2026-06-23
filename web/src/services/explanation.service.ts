import { apiFetch } from '@/lib/api'
import {
  ListTransactionTypesResponseSchema,
  ListCategoriesForTypeResponseSchema,
  TransactionExplanationsResponseSchema,
  type TransactionType,
  type ExplanationCategory,
  type TransactionExplanations,
  type CreateExplanationRequest,
} from '@/types/explanation'

// The explain/reconcile API. The bearer token is attached by apiFetch (401 handled
// there). Mutations return the line's refreshed reconcile state + explanations, so
// the caller patches the statement row in place without a full refetch.

// An empty explanations list may arrive as null → []. parseExpl normalises it.
function parseExpl(data: unknown): TransactionExplanations {
  const p = TransactionExplanationsResponseSchema.parse(data)
  return { ...p, explanations: p.explanations ?? [] }
}

// GET /api/v1/transaction-types — the explain "Type" dropdown (all 18, flagged supported).
export async function listTransactionTypes(): Promise<TransactionType[]> {
  const data = await apiFetch<unknown>('/transaction-types', { method: 'GET' })
  return ListTransactionTypesResponseSchema.parse(data).transaction_types ?? []
}

// GET /api/v1/transaction-types/:code/categories — the accounts a type offers.
// Cached per type code (the mapping is static reference data for the session).
const categoriesCache = new Map<string, ExplanationCategory[]>()
export async function listCategoriesForType(code: string): Promise<ExplanationCategory[]> {
  const cached = categoriesCache.get(code)
  if (cached) return cached
  const data = await apiFetch<unknown>(`/transaction-types/${encodeURIComponent(code)}/categories`, { method: 'GET' })
  const list = ListCategoriesForTypeResponseSchema.parse(data).categories ?? []
  categoriesCache.set(code, list)
  return list
}

// GET …/transactions/:txnId/explanations — a line's explanations + reconcile state.
export async function listExplanations(accountId: string, txnId: string): Promise<TransactionExplanations> {
  const data = await apiFetch<unknown>(
    `/bank-accounts/${encodeURIComponent(accountId)}/transactions/${encodeURIComponent(txnId)}/explanations`,
    { method: 'GET' },
  )
  return parseExpl(data)
}

// POST …/explanations — explain (part of) a line (owner/admin). 422 (bad type/category,
// over-explain) surfaces as an ApiError for the panel to show.
export async function createExplanation(accountId: string, txnId: string, payload: CreateExplanationRequest): Promise<TransactionExplanations> {
  const data = await apiFetch<unknown>(
    `/bank-accounts/${encodeURIComponent(accountId)}/transactions/${encodeURIComponent(txnId)}/explanations`,
    { method: 'POST', body: payload },
  )
  return parseExpl(data)
}

// PUT …/explanations/:explId — edit one explanation (owner/admin).
export async function updateExplanation(accountId: string, txnId: string, explId: string, payload: CreateExplanationRequest): Promise<TransactionExplanations> {
  const data = await apiFetch<unknown>(
    `/bank-accounts/${encodeURIComponent(accountId)}/transactions/${encodeURIComponent(txnId)}/explanations/${encodeURIComponent(explId)}`,
    { method: 'PUT', body: payload },
  )
  return parseExpl(data)
}

// DELETE …/explanations/:explId — un-explain a portion (owner/admin).
export async function deleteExplanation(accountId: string, txnId: string, explId: string): Promise<TransactionExplanations> {
  const data = await apiFetch<unknown>(
    `/bank-accounts/${encodeURIComponent(accountId)}/transactions/${encodeURIComponent(txnId)}/explanations/${encodeURIComponent(explId)}`,
    { method: 'DELETE' },
  )
  return parseExpl(data)
}
