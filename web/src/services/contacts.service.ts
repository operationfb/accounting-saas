import { apiFetch } from '@/lib/api'
import { ListContactsResponseSchema, type Contact } from '@/types/contact'

// GET /api/v1/contacts — every contact in the caller's organisation. The bearer
// token is attached by apiFetch, and a 401 (expired/invalid token) is handled
// there (logout + redirect to /login). An empty list may arrive as null, so we
// default to [].
export async function listContacts(): Promise<Contact[]> {
  const data = await apiFetch<unknown>('/contacts', { method: 'GET' })
  return ListContactsResponseSchema.parse(data).contacts ?? []
}
