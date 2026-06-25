import { z } from 'zod'

// Mirrors the backend's FreeAgentStatusResponse (integration_service.go). The
// settings screen renders these. has_credentials is a GLOBAL fact (the operator
// configured the app in provider_credentials); connected is per-org. No secrets.
// connected_at is RFC3339 and present only when connected.
export const FreeAgentStatusSchema = z.object({
  has_credentials: z.boolean(),
  connected: z.boolean(),
  connected_at: z.string().nullish(),
})
export type FreeAgentStatus = z.infer<typeof FreeAgentStatusSchema>

// GET /api/v1/integrations/freeagent → { "integration": {...} }.
export const GetFreeAgentStatusResponseSchema = z.object({
  integration: FreeAgentStatusSchema,
})

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

// HMRC MTD integration status — mirrors integrations.StatusResponse on the backend.
// Identical shape to FreeAgentStatus (same generic endpoint, different provider slug).
export const HmrcStatusSchema = z.object({
  has_credentials: z.boolean(),
  connected: z.boolean(),
  connected_at: z.string().nullish(),
})
export type HmrcStatus = z.infer<typeof HmrcStatusSchema>

// GET /api/v1/integrations/hmrc → { "integration": {...} }.
export const GetHmrcStatusResponseSchema = z.object({
  integration: HmrcStatusSchema,
})
