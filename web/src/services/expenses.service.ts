import { apiFetch, apiUpload, apiDownload } from '@/lib/api'
import {
  ListExpensesResponseSchema,
  GetExpenseResponseSchema,
  ListCategoriesResponseSchema,
  ListVatRatesResponseSchema,
  CreateExpenseResponseSchema,
  CaptureExpenseResponseSchema,
  type Expense,
  type ExpenseDetail,
  type ExpenseCategory,
  type VatRate,
  type CreateExpenseRequest,
} from '@/types/expense'

// GET /api/v1/expenses — the bearer token is attached by apiFetch, and a 401
// (expired/invalid token) is handled there (logout + redirect to /login).
export async function listExpenses(): Promise<Expense[]> {
  const data = await apiFetch<unknown>('/expenses', { method: 'GET' })
  const parsed = ListExpensesResponseSchema.parse(data)
  return parsed.expenses ?? []
}

// GET /api/v1/expenses/export — download the caller's visible expenses as a CSV
// Blob (owners/admins get the whole org; members only their own — the backend
// enforces it). Returns the raw Blob; the VIEW owns turning it into a browser
// download (filename + anchor click). Bearer/401 handled by apiDownload.
export async function exportExpenses(): Promise<Blob> {
  return apiDownload('/expenses/export')
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

// GET /api/v1/vat-rates — VAT rates valid today for the caller's org country
// (populates the entry form's VAT rate picker).
export async function listVatRates(): Promise<VatRate[]> {
  const data = await apiFetch<unknown>('/vat-rates', { method: 'GET' })
  return ListVatRatesResponseSchema.parse(data).vat_rates ?? []
}

// POST /api/v1/expenses — create an expense. Returns the created (lean) expense;
// the caller uses its id to navigate. A 422 (bad date/decimal/uuid) is thrown as
// an ApiError for the form to display.
export async function createExpense(payload: CreateExpenseRequest): Promise<Expense> {
  const data = await apiFetch<unknown>('/expenses', { method: 'POST', body: payload })
  return CreateExpenseResponseSchema.parse(data).expense
}

// PUT /api/v1/expenses/:id — update an editable (DRAFT/REJECTED) expense. Same
// payload as create; returns the updated (lean) expense. A 409 means the expense
// is no longer editable; 422 a bad field — both surfaced as an ApiError.
export async function updateExpense(id: string, payload: CreateExpenseRequest): Promise<Expense> {
  const data = await apiFetch<unknown>(`/expenses/${encodeURIComponent(id)}`, {
    method: 'PUT',
    body: payload,
  })
  return CreateExpenseResponseSchema.parse(data).expense
}

// The status transitions the backend accepts on POST /expenses/:id/status
// (expense_status.go). `reject` additionally requires a note.
export type ExpenseStatusAction = 'submit' | 'approve' | 'reject' | 'reopen'

// POST /api/v1/expenses/:id/status — move an expense along the approval chain:
//   submit   DRAFT → SUBMITTED     (claimant on own, or owner/admin)
//   approve  SUBMITTED → APPROVED  (owner/admin only)
//   reject   SUBMITTED → REJECTED  (owner/admin only; rejectionNote required)
//   reopen   REJECTED → DRAFT      (claimant on own, or owner/admin)
// Returns the updated (lean) expense. The backend enforces the legal transition
// (409 if the expense isn't in the required `from` status), authorisation (403),
// and the rejection-note rule (422) — all surfaced as an ApiError. The detail view
// re-fetches the rich record afterwards rather than relying on this lean shape.
export async function changeExpenseStatus(
  id: string,
  action: ExpenseStatusAction,
  rejectionNote?: string,
): Promise<Expense> {
  const body: { action: ExpenseStatusAction; rejection_note?: string } = { action }
  if (action === 'reject') body.rejection_note = rejectionNote ?? ''
  const data = await apiFetch<unknown>(`/expenses/${encodeURIComponent(id)}/status`, {
    method: 'POST',
    body,
  })
  return CreateExpenseResponseSchema.parse(data).expense
}

// POST /api/v1/expenses/capture — "Smart Upload". Uploads a receipt/invoice; the
// backend creates a skeleton DRAFT (needs_review=true), attaches the file, and runs
// background OCR. Multipart, like uploadAttachment — `document_type` must be the
// exact lowercase the backend expects. Returns the RICH detail (with the embedded
// attachment) so the caller gets the new id to redirect to and poll.
export async function captureExpense(
  file: File,
  documentType: 'receipt' | 'invoice',
): Promise<ExpenseDetail> {
  const form = new FormData()
  form.append('file', file)
  form.append('document_type', documentType)
  const data = await apiUpload<unknown>('/expenses/capture', form)
  return CaptureExpenseResponseSchema.parse(data).expense
}

// DELETE /api/v1/expenses/:id — soft-deletes a DRAFT/REJECTED expense. Used to discard
// an abandoned Smart Upload skeleton when the user cancels. 204, no body — apiFetch
// tolerates the empty response. A 409 (no longer DRAFT/REJECTED) or 403 surfaces as
// an ApiError, which the caller swallows so navigation still happens.
export async function deleteExpense(id: string): Promise<void> {
  await apiFetch<unknown>(`/expenses/${encodeURIComponent(id)}`, { method: 'DELETE' })
}
