import { apiFetch } from '@/lib/api'
import {
  ListBillsResponseSchema,
  GetBillResponseSchema,
  ListBillCategoriesResponseSchema,
  type Bill,
  type BillCategory,
  type CreateBillRequest,
} from '@/types/bill'

// GET /api/v1/bills — the org's bills, newest first. The bearer token is attached
// by apiFetch, and a 401 is handled there (logout + redirect). An empty list may
// arrive as null, so default to [].
export async function listBills(): Promise<Bill[]> {
  const data = await apiFetch<unknown>('/bills', { method: 'GET' })
  return ListBillsResponseSchema.parse(data).bills ?? []
}

// GET /api/v1/bills/outstanding — the org's bills that still owe money (due > 0).
// Backs the banking "Bill Payment" explanation picker (mirrors listOutstandingInvoices).
export async function listOutstandingBills(): Promise<Bill[]> {
  const data = await apiFetch<unknown>('/bills/outstanding', { method: 'GET' })
  return ListBillsResponseSchema.parse(data).bills ?? []
}

// GET /api/v1/bills/:id — one bill. A 404 surfaces as an ApiError for the caller.
export async function getBill(id: string): Promise<Bill> {
  const data = await apiFetch<unknown>(`/bills/${encodeURIComponent(id)}`, { method: 'GET' })
  return GetBillResponseSchema.parse(data).bill
}

// POST /api/v1/bills — create a bill. Returns the created bill; the caller navigates
// back to the list.
export async function createBill(payload: CreateBillRequest): Promise<Bill> {
  const data = await apiFetch<unknown>('/bills', { method: 'POST', body: payload })
  return GetBillResponseSchema.parse(data).bill
}

// PUT /api/v1/bills/:id — full update. Allowed only while the bill is UNPAID (the
// backend returns 409 once a payment is recorded against it).
export async function updateBill(id: string, payload: CreateBillRequest): Promise<Bill> {
  const data = await apiFetch<unknown>(`/bills/${encodeURIComponent(id)}`, {
    method: 'PUT',
    body: payload,
  })
  return GetBillResponseSchema.parse(data).bill
}

// DELETE /api/v1/bills/:id — soft-delete (UNPAID only; 409 otherwise). 204 No
// Content, so there's nothing to return.
export async function deleteBill(id: string): Promise<void> {
  await apiFetch<unknown>(`/bills/${encodeURIComponent(id)}`, { method: 'DELETE' })
}

// GET /api/v1/bill-categories — the "Spending Category" picker (the spending subset
// of the org's Chart of Accounts). An empty list may arrive as null → [].
export async function listBillCategories(): Promise<BillCategory[]> {
  const data = await apiFetch<unknown>('/bill-categories', { method: 'GET' })
  return ListBillCategoriesResponseSchema.parse(data).bill_categories ?? []
}
