import { apiFetch } from '@/lib/api'
import {
  ListExpensesResponseSchema,
  GetExpenseResponseSchema,
  type Expense,
  type ExpenseDetail,
} from '@/types/expense'

// GET /api/v1/expenses — the bearer token is attached by apiFetch, and a 401
// (expired/invalid token) is handled there (logout + redirect to /login).
export async function listExpenses(): Promise<Expense[]> {
  const data = await apiFetch<unknown>('/expenses', { method: 'GET' })
  const parsed = ListExpensesResponseSchema.parse(data)
  return parsed.expenses ?? []
}

// GET /api/v1/expenses/:id — returns the RICH detail (v_expenses_full). Same
// bearer/401 handling as listExpenses. The backend returns 404 (not found),
// 403 (forbidden) or 422 (bad id), which the caller surfaces as an error state.
export async function getExpense(id: string): Promise<ExpenseDetail> {
  const data = await apiFetch<unknown>(`/expenses/${encodeURIComponent(id)}`, { method: 'GET' })
  return GetExpenseResponseSchema.parse(data).expense
}
