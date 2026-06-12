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

// POST /auth/login success body.
// `expires_in` is OPTIONAL: the backend does not send it today, but if it is
// added later the store will use it for proactive expiry handling. (The PASETO
// token itself is encrypted, so the SPA can't read the expiry from it.)
export const LoginResponseSchema = z.object({
  access_token: z.string(),
  user: UserSchema,
  expires_in: z.number().optional(),
})
export type LoginResponse = z.infer<typeof LoginResponseSchema>

export interface LoginRequest {
  email: string
  password: string
}
