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
  email_verified: z.boolean(),
})
export type User = z.infer<typeof UserSchema>

// The organisation the session is scoped to. Comes from the login RESPONSE, not
// the token — the PASETO token is encrypted (the SPA can't read it) and only
// carries the org id anyway, not its name/country.
// country_code (ISO 3166-1 alpha-2, e.g. 'GB') is REQUIRED: it drives
// country-scoped features such as which VAT rates apply. If the backend ever
// omits it, this parse throws and login fails — country_code is a must-have.
export const OrganisationSchema = z.object({
  id: z.string(),
  name: z.string(),
  country_code: z.string(),
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
