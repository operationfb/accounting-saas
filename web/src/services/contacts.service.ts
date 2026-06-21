import { apiFetch } from '@/lib/api'
import {
  ListContactsResponseSchema,
  GetContactResponseSchema,
  type Contact,
  type CreateContactRequest,
} from '@/types/contact'

// GET /api/v1/contacts — every contact in the caller's organisation. The bearer
// token is attached by apiFetch, and a 401 (expired/invalid token) is handled
// there (logout + redirect to /login). An empty list may arrive as null, so we
// default to [].
export async function listContacts(): Promise<Contact[]> {
  const data = await apiFetch<unknown>('/contacts', { method: 'GET' })
  return ListContactsResponseSchema.parse(data).contacts ?? []
}

// GET /api/v1/contacts/:id — one contact, used to pre-fill the edit form. A 404
// (unknown id / other org) surfaces as an ApiError for the caller to show.
export async function getContact(id: string): Promise<Contact> {
  const data = await apiFetch<unknown>(`/contacts/${encodeURIComponent(id)}`, { method: 'GET' })
  return GetContactResponseSchema.parse(data).contact
}

// POST /api/v1/contacts — create a contact. Returns the created contact; the
// caller uses it to navigate. A 422 (e.g. bad charge_vat/country) or 400 (bad
// email binding) is thrown as an ApiError for the form to display.
export async function createContact(payload: CreateContactRequest): Promise<Contact> {
  const data = await apiFetch<unknown>('/contacts', { method: 'POST', body: payload })
  return GetContactResponseSchema.parse(data).contact
}

// PUT /api/v1/contacts/:id — full update of an editable contact. Same payload as
// create; returns the updated contact. A 403 (not creator/admin) or 404 surfaces
// as an ApiError.
export async function updateContact(id: string, payload: CreateContactRequest): Promise<Contact> {
  const data = await apiFetch<unknown>(`/contacts/${encodeURIComponent(id)}`, {
    method: 'PUT',
    body: payload,
  })
  return GetContactResponseSchema.parse(data).contact
}

// DELETE /api/v1/contacts/:id — soft-deletes the contact (the row is kept but
// hidden everywhere). Returns 204 with no body. The backend refuses with a 409
// if the contact is still in use (e.g. referenced by a project); that surfaces
// as an ApiError for the caller to show. Mirrors deleteExpense.
export async function deleteContact(id: string): Promise<void> {
  await apiFetch<unknown>(`/contacts/${encodeURIComponent(id)}`, { method: 'DELETE' })
}
