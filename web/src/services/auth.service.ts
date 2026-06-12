import { apiFetch } from '@/lib/api'
import { LoginResponseSchema, type LoginResponse } from '@/types/auth'

// Calls POST /auth/login.
//   - auth:false           → no bearer token to send yet
//   - skipAuthRedirect:true → a 401 here is "bad credentials", shown on the
//     login form, not a session-expiry redirect.
export async function login(email: string, password: string): Promise<LoginResponse> {
  const data = await apiFetch<unknown>('/auth/login', {
    method: 'POST',
    auth: false,
    skipAuthRedirect: true,
    body: { email, password },
  })
  // Validate the response shape at the boundary — throws if the API drifts.
  return LoginResponseSchema.parse(data)
}
