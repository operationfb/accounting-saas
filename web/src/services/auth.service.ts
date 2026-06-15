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

// Calls POST /auth/forgot-password. The backend ALWAYS returns 200 with a generic
// message and never reveals whether the email is registered (no account
// enumeration), so the caller just shows a neutral "check your inbox" confirmation.
//   - auth:false           → unauthenticated endpoint, no bearer token
//   - skipAuthRedirect:true → a non-2xx here is a form error, not a session expiry
export async function forgotPassword(email: string): Promise<{ message: string }> {
  return apiFetch<{ message: string }>('/auth/forgot-password', {
    method: 'POST',
    auth: false,
    skipAuthRedirect: true,
    body: { email },
  })
}

// Calls POST /auth/reset-password/:token. The raw token is the code from the
// emailed link (a URL path segment). Resolves with the success message; throws an
// ApiError on a 400 — an invalid/expired/used link, or a password under 8 chars.
export async function resetPassword(token: string, password: string): Promise<{ message: string }> {
  return apiFetch<{ message: string }>(`/auth/reset-password/${encodeURIComponent(token)}`, {
    method: 'POST',
    auth: false,
    skipAuthRedirect: true,
    body: { password },
  })
}
