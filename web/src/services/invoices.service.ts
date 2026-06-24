import { apiFetch } from '@/lib/api'
import {
  ListInvoicesResponseSchema,
  GetInvoiceResponseSchema,
  type Invoice,
  type CreateInvoiceRequest,
} from '@/types/invoice'

// GET /api/v1/invoices — the org's invoices, newest first (header only, no line
// items). The bearer token is attached by apiFetch, and a 401 is handled there
// (logout + redirect). An empty list may arrive as null, so default to [].
export async function listInvoices(): Promise<Invoice[]> {
  const data = await apiFetch<unknown>('/invoices', { method: 'GET' })
  return ListInvoicesResponseSchema.parse(data).invoices ?? []
}

// GET /api/v1/invoices/:id — one invoice WITH its line items. A 404 surfaces as an
// ApiError for the caller to show.
export async function getInvoice(id: string): Promise<Invoice> {
  const data = await apiFetch<unknown>(`/invoices/${encodeURIComponent(id)}`, { method: 'GET' })
  return GetInvoiceResponseSchema.parse(data).invoice
}

// GET /api/v1/invoices/next-reference — the suggested reference for a NEW invoice
// (the org's next sequential number, e.g. "001"). The create form pre-fills it; the
// user may overwrite it.
export async function getNextInvoiceReference(): Promise<string> {
  const data = await apiFetch<{ reference?: string }>('/invoices/next-reference', { method: 'GET' })
  return data?.reference ?? ''
}

// POST /api/v1/invoices — create an invoice (starts DRAFT). Returns the created
// invoice; the caller navigates to its detail with the id.
export async function createInvoice(payload: CreateInvoiceRequest): Promise<Invoice> {
  const data = await apiFetch<unknown>('/invoices', { method: 'POST', body: payload })
  return GetInvoiceResponseSchema.parse(data).invoice
}

// PUT /api/v1/invoices/:id — full update. The backend REBUILDS all line items from
// payload.items, so always pass the FULL current list. DRAFT only (409 otherwise).
export async function updateInvoice(id: string, payload: CreateInvoiceRequest): Promise<Invoice> {
  const data = await apiFetch<unknown>(`/invoices/${encodeURIComponent(id)}`, {
    method: 'PUT',
    body: payload,
  })
  return GetInvoiceResponseSchema.parse(data).invoice
}

// DELETE /api/v1/invoices/:id — soft-delete (DRAFT only; 409 otherwise). 204 No
// Content, so there's nothing to return.
export async function deleteInvoice(id: string): Promise<void> {
  await apiFetch<unknown>(`/invoices/${encodeURIComponent(id)}`, { method: 'DELETE' })
}

// The status transitions the backend accepts on POST /invoices/:id/status
// (internal/invoices/status.go). The UI currently surfaces issue + reopen; the
// rest are valid backend actions whose buttons are deferred.
export type InvoiceStatusAction = 'issue' | 'schedule' | 'send' | 'write_off' | 'refund' | 'reopen'

// POST /api/v1/invoices/:id/status — move the lifecycle:
//   issue   DRAFT → SENT       reopen  SCHEDULED|SENT → DRAFT
//   (schedule / send / write_off / refund also valid — not yet surfaced)
// Returns the updated invoice. The backend enforces the legal transition (409),
// authorisation (403) and tenancy (404) — all surfaced as an ApiError.
export async function changeInvoiceStatus(id: string, action: InvoiceStatusAction): Promise<Invoice> {
  const data = await apiFetch<unknown>(`/invoices/${encodeURIComponent(id)}/status`, {
    method: 'POST',
    body: { action },
  })
  return GetInvoiceResponseSchema.parse(data).invoice
}
