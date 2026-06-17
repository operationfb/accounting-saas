import { apiFetch } from '@/lib/api'
import { GetProfileResponseSchema, InboxAddressSchema, type UpdateProfileRequest } from '@/types/user'
import type { User } from '@/types/auth'

// GET /api/v1/profile — the caller's own "My Details" (first/last name + the
// read-only login email). The user is taken from the bearer token (no id in the
// path), so a caller can only ever read their own profile. apiFetch attaches the
// token and handles a 401.
export async function getProfile(): Promise<User> {
  const data = await apiFetch<unknown>('/profile', { method: 'GET' })
  return GetProfileResponseSchema.parse(data).user
}

// PUT /api/v1/profile — update the caller's first/last name. Returns the updated
// user. A 400 (missing name) or 422 (blank name) surfaces as an ApiError for the
// form to display.
export async function updateProfile(payload: UpdateProfileRequest): Promise<User> {
  const data = await apiFetch<unknown>('/profile', { method: 'PUT', body: payload })
  return GetProfileResponseSchema.parse(data).user
}

// GET /api/v1/inbox-address — the caller's Mailgun receipt-forwarding address for
// the current organisation. Returns { enabled, address }; enabled is false (and
// address empty) when the email-to-expense channel isn't configured.
export async function getInboxAddress(): Promise<{ enabled: boolean; address: string }> {
  const data = await apiFetch<unknown>('/inbox-address', { method: 'GET' })
  return InboxAddressSchema.parse(data)
}
