import { z } from 'zod'

// The safe, public user shape returned by POST /auth/login. Mirrors the
// backend's userResponse — no password hash, timestamps, etc.
export const UserSchema = z.object({
  id: z.string(),
  email: z.string(),
  first_name: z.string(),
  last_name: z.string(),
  phone: z.string().nullish(),
  avatar_url: z.string().nullish(),
  // Payroll-identity fields (future payroll module). Optional/nullable; the login
  // response carries them, so auth.user has them for the self User Details form.
  national_insurance_number: z.string().nullish(),
  utr: z.string().nullish(),
  date_of_birth: z.string().nullish(), // ISO YYYY-MM-DD
  // Personal/home address (future payroll module). Optional/nullable.
  address_line_1: z.string().nullish(),
  address_line_2: z.string().nullish(),
  address_line_3: z.string().nullish(),
  address_line_4: z.string().nullish(),
  postcode: z.string().nullish(),
  email_verified: z.boolean(),
})
export type User = z.infer<typeof UserSchema>

// The caller's membership role in the scoped organisation. Mirrors the backend
// Postgres `organisation_role` enum. The role is per-membership (a user can be
// owner in one org and member in another), so it lives on the organisation, not
// the user. Used to drive role-based UI (e.g. hiding admin-only actions).
export const RoleSchema = z.enum(['owner', 'admin', 'member', 'accountant', 'read_only'])
export type Role = z.infer<typeof RoleSchema>

// The organisation the session is scoped to. Comes from the login RESPONSE, not
// the token — the PASETO token is encrypted (the SPA can't read it) and only
// carries the org id anyway, not its name/country.
// country_code (ISO 3166-1 alpha-2, e.g. 'GB') is REQUIRED: it drives
// country-scoped features such as which VAT rates apply. If the backend ever
// omits it, this parse throws and login fails — country_code is a must-have.
// role is REQUIRED too: the backend always returns it on a successful login.
export const OrganisationSchema = z.object({
  id: z.string(),
  name: z.string(),
  country_code: z.string(),
  role: RoleSchema,
})
export type Organisation = z.infer<typeof OrganisationSchema>

// POST /auth/login success body.
// `organisation` is the org the token is scoped to. The backend now fails login
// unless it can resolve an organisation (with a country_code), so on success
// this is always present; kept nullish for defensive parsing only.
// `expires_in` is OPTIONAL: the backend does not send it today, but if added
// later the store will use it for proactive expiry handling.
export const LoginResponseSchema = z.object({
  access_token: z.string(),
  user: UserSchema,
  organisation: OrganisationSchema.nullish(),
  expires_in: z.number().optional(),
})
export type LoginResponse = z.infer<typeof LoginResponseSchema>

export interface LoginRequest {
  email: string
  password: string
}
