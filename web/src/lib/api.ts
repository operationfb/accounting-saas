// Thin typed fetch wrapper around the Go API.
//   - prefixes VITE_API_BASE_URL
//   - attaches the PASETO bearer token from the auth store
//   - normalises the backend's two error shapes into a single ApiError
//   - reacts to 401 (expired/invalid token) by logging out + redirecting to
//     /login — EXCEPT for the login call itself (skipAuthRedirect).
//
// NOTE on the import cycle: this file imports the auth store and the router, and
// both (indirectly) import this file. That's fine because we only *use* them
// inside apiFetch() at call time — never at module load — so the bindings are
// fully initialised by the time a request actually runs.
import { useAuthStore } from '@/stores/auth'
import router from '@/router'

const BASE_URL = (import.meta.env.VITE_API_BASE_URL as string) ?? '/api/v1'

// A single error shape every caller can rely on, regardless of which backend
// shape produced it.
export interface ApiError {
  status: number
  code?: string
  message: string
}

export interface RequestOptions {
  method?: 'GET' | 'POST' | 'PUT' | 'DELETE'
  body?: unknown
  // Attach the bearer token (default true). The login call sets this false.
  auth?: boolean
  // Skip the global 401 → logout+redirect. The login call sets this true, so a
  // 401 there (bad credentials) is shown on the form instead of bouncing.
  skipAuthRedirect?: boolean
}

export async function apiFetch<T>(path: string, options: RequestOptions = {}): Promise<T> {
  const { method = 'GET', body, auth = true, skipAuthRedirect = false } = options

  const headers: Record<string, string> = { 'Content-Type': 'application/json' }
  if (auth) {
    const token = useAuthStore().token
    if (token) headers.Authorization = `Bearer ${token}`
  }

  const res = await fetch(`${BASE_URL}${path}`, {
    method,
    headers,
    body: body === undefined ? undefined : JSON.stringify(body),
  })

  // The body may be empty (e.g. 204) — tolerate a failed parse.
  let data: unknown = null
  try {
    data = await res.json()
  } catch {
    data = null
  }

  if (!res.ok) {
    const err = normaliseError(res.status, data)

    // 401 = not authenticated (expired/invalid token). Handle it ONCE: clear
    // the session and bounce to /login, remembering where we were. Skip if the
    // caller opted out (login) or we're already on /login (no redirect loops).
    if (res.status === 401 && !skipAuthRedirect) {
      useAuthStore().logout()
      const current = router.currentRoute.value
      if (current.name !== 'login') {
        void router.replace({ name: 'login', query: { redirect: current.fullPath } })
      }
    }
    throw err
  }

  return data as T
}

// The backend returns errors in two shapes:
//   binding / login: { "error": "some string" }
//   AppError:        { "error": { "code": "...", "message": "..." } }
function normaliseError(status: number, data: unknown): ApiError {
  const error = (data as { error?: unknown } | null)?.error
  if (typeof error === 'string') {
    return { status, message: error }
  }
  if (error && typeof error === 'object') {
    const e = error as { code?: string; message?: string }
    return { status, code: e.code, message: e.message ?? 'Request failed' }
  }
  return { status, message: `Request failed (${status})` }
}
