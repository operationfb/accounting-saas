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
  method?: 'GET' | 'POST' | 'PUT' | 'PATCH' | 'DELETE'
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
  if (auth) attachBearer(headers)

  const res = await fetch(`${BASE_URL}${path}`, {
    method,
    headers,
    body: body === undefined ? undefined : JSON.stringify(body),
  })

  return handleResponse<T>(res, skipAuthRedirect)
}

// Multipart upload (e.g. a receipt file) via FormData. Unlike apiFetch we
// DELIBERATELY don't set Content-Type: the browser must set it itself so it can
// add the multipart boundary — setting it by hand corrupts the request. The
// bearer token and the 401/error handling are otherwise identical to apiFetch.
export async function apiUpload<T>(
  path: string,
  formData: FormData,
  options: { method?: 'POST' | 'PUT'; skipAuthRedirect?: boolean } = {},
): Promise<T> {
  const { method = 'POST', skipAuthRedirect = false } = options

  const headers: Record<string, string> = {}
  attachBearer(headers)

  const res = await fetch(`${BASE_URL}${path}`, { method, headers, body: formData })

  return handleResponse<T>(res, skipAuthRedirect)
}

// Download a non-JSON response (e.g. the CSV export) as a Blob. The bearer-attach
// and the 401 → logout+redirect behaviour are identical to apiFetch, but on
// success we hand back the raw Blob instead of parsing JSON. On a NON-2xx the
// body IS JSON (the backend's error shape), so we parse + normalise it the same
// way handleResponse does — the caller catches a plain ApiError either way.
export async function apiDownload(
  path: string,
  options: { skipAuthRedirect?: boolean } = {},
): Promise<Blob> {
  const { skipAuthRedirect = false } = options

  const headers: Record<string, string> = {}
  attachBearer(headers)

  const res = await fetch(`${BASE_URL}${path}`, { method: 'GET', headers })

  if (!res.ok) {
    let data: unknown = null
    try {
      data = await res.json()
    } catch {
      data = null
    }
    const err = normaliseError(res.status, data)
    if (res.status === 401 && !skipAuthRedirect) {
      useAuthStore().logout()
      const current = router.currentRoute.value
      if (current.name !== 'login') {
        void router.replace({ name: 'login', query: { redirect: current.fullPath } })
      }
    }
    throw err
  }

  return res.blob()
}

// Attach the PASETO bearer token from the auth store, when we have one.
function attachBearer(headers: Record<string, string>): void {
  const token = useAuthStore().token
  if (token) headers.Authorization = `Bearer ${token}`
}

// Shared response handling for apiFetch + apiUpload: parse the (possibly empty)
// JSON body, and on a non-2xx normalise the error — reacting ONCE to a 401 by
// logging out + redirecting to /login (unless the caller opted out, or we're
// already there, so there are no redirect loops).
async function handleResponse<T>(res: Response, skipAuthRedirect: boolean): Promise<T> {
  // The body may be empty (e.g. 204) — tolerate a failed parse.
  let data: unknown = null
  try {
    data = await res.json()
  } catch {
    data = null
  }

  if (!res.ok) {
    const err = normaliseError(res.status, data)
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
