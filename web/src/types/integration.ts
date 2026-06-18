import { z } from 'zod'

// Mirrors the backend's FreeAgentStatusResponse (integration_service.go). The
// settings screen renders these; the OAuth secrets are NEVER returned. connected_at
// is RFC3339 and present only when connected.
export const FreeAgentStatusSchema = z.object({
  has_credentials: z.boolean(),
  connected: z.boolean(),
  connected_at: z.string().nullish(),
})
export type FreeAgentStatus = z.infer<typeof FreeAgentStatusSchema>

// GET/PUT /api/v1/integrations/freeagent → { "integration": {...} }.
export const GetFreeAgentStatusResponseSchema = z.object({
  integration: FreeAgentStatusSchema,
})

// PUT /api/v1/integrations/freeagent body — the org's FreeAgent OAuth app
// credentials (from their FreeAgent developer dashboard). Both required.
export interface SaveFreeAgentCredentialsRequest {
  client_id: string
  client_secret: string
}

// GET /api/v1/freeagent/connect → { "authorize_url": "..." }. The SPA navigates the
// browser there (window.location) to start the OAuth approve step — it's JSON, not a
// 302, because a top-level redirect can't carry the SPA's bearer token.
export const ConnectResponseSchema = z.object({
  authorize_url: z.string(),
})

// Mirrors FreeAgentPushStatusResponse (integration_service.go) — the per-expense
// push outcome behind the detail-page "Pushed ✓ / Failed ⚠" badge. `state` is the
// discriminator; the optional fields are populated per state. `connected` says the
// org has a live FreeAgent connection (so the UI can show "Pushing…" vs nothing).
export const FreeAgentPushStatusSchema = z.object({
  state: z.enum(['pushed', 'failed', 'none']),
  external_url: z.string().nullish(),
  error: z.string().nullish(),
  pushed_at: z.string().nullish(),
  connected: z.boolean(),
})
export type FreeAgentPushStatus = z.infer<typeof FreeAgentPushStatusSchema>

// GET /api/v1/integrations/freeagent/expenses/:id/push → { "push": {...} }.
export const GetFreeAgentPushStatusResponseSchema = z.object({
  push: FreeAgentPushStatusSchema,
})
