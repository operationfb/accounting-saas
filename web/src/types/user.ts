import { z } from 'zod'
import { UserSchema } from '@/types/auth'

// Types for the signed-in user's own "My Details" screen (GET/PUT /api/v1/profile
// and GET /api/v1/inbox-address). Like Company Details this is a SINGLETON: the
// user is taken from the bearer token, so there is no id in the URL — a caller
// can only ever read/edit themselves.

// GET /api/v1/profile and PUT /api/v1/profile both return { "user": {...} } where
// the user is the SAME safe shape the login response uses, so we reuse UserSchema
// (id, email, first_name, last_name, phone?, avatar_url?, email_verified) rather
// than redefining it.
export const GetProfileResponseSchema = z.object({
  user: UserSchema,
})

// PUT body. Mirrors the backend's UpdateProfileRequest (internal/userauth). The
// login email is read-only and phone/avatar_url are preserved server-side (not
// sent by this form). Both names are required; the payroll fields are optional
// (null clears the column) and validated server-side (NINO/UTR shape, DOB range).
export interface UpdateProfileRequest {
  first_name: string
  last_name: string
  national_insurance_number?: string | null
  utr?: string | null
  date_of_birth?: string | null // ISO YYYY-MM-DD
  address_line_1?: string | null
  address_line_2?: string | null
  address_line_3?: string | null
  address_line_4?: string | null
  postcode?: string | null
}

// GET /api/v1/inbox-address — the caller's Mailgun receipt-forwarding address.
// `enabled` is false when the channel isn't configured (no INBOX_DOMAIN), in
// which case `address` is empty and the UI hides the feature.
export const InboxAddressSchema = z.object({
  enabled: z.boolean(),
  address: z.string(),
})
export type InboxAddress = z.infer<typeof InboxAddressSchema>
