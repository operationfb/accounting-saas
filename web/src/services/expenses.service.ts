import { apiFetch } from '@/lib/api'
import {
  ListExpensesResponseSchema,
  GetExpenseResponseSchema,
  ListCategoriesResponseSchema,
  CreateExpenseResponseSchema,
  type Expense,
  type ExpenseDetail,
  type ExpenseCategory,
  type CreateExpenseRequest,
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

// GET /api/v1/expense-categories — active categories for the caller's org
// (populates the entry form's category picker).
export async function listCategories(): Promise<ExpenseCategory[]> {
  const data = await apiFetch<unknown>('/expense-categories', { method: 'GET' })
  return ListCategoriesResponseSchema.parse(data).expense_categories ?? []
}

// POST /api/v1/expenses — create an expense. Returns the created (lean) expense;
// the caller uses its id to navigate. A 422 (bad date/decimal/uuid) is thrown as
// an ApiError for the form to display.
export async function createExpense(payload: CreateExpenseRequest): Promise<Expense> {
  const data = await apiFetch<unknown>('/expenses', { method: 'POST', body: payload })
  return CreateExpenseResponseSchema.parse(data).expense
}
