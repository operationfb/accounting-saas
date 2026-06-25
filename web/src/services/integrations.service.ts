import { apiFetch } from '@/lib/api'
import {
  GetFreeAgentStatusResponseSchema,
  GetHmrcStatusResponseSchema,
  ConnectResponseSchema,
  GetFreeAgentPushStatusResponseSchema,
  type FreeAgentStatus,
  type FreeAgentPushStatus,
  type HmrcStatus,
} from '@/types/integration'

// GET /api/v1/integrations/freeagent — the org's FreeAgent connection status. The
// bearer token is attached by apiFetch; a 401 is handled there. OWNER/ADMIN only on
// the backend, so a 403 (member/non-member) surfaces as an ApiError. No secrets.
export async function getFreeAgentStatus(): Promise<FreeAgentStatus> {
  const data = await apiFetch<unknown>('/integrations/freeagent', { method: 'GET' })
  return GetFreeAgentStatusResponseSchema.parse(data).integration
}

// DELETE /api/v1/integrations/freeagent — disconnect this org: drop its tokens. The
// GLOBAL app credentials (provider_credentials) are untouched, so reconnecting is one
// click. 204, no body — apiFetch tolerates the empty response. Owner/admin only.
export async function disconnectFreeAgent(): Promise<void> {
  await apiFetch<unknown>('/integrations/freeagent', { method: 'DELETE' })
}

// GET /api/v1/freeagent/connect — the FreeAgent authorize URL the SPA navigates to.
// Owner/admin only; a 422 means FreeAgent isn't configured yet (no global app creds).
export async function getFreeAgentConnectUrl(): Promise<string> {
  const data = await apiFetch<unknown>('/freeagent/connect', { method: 'GET' })
  return ConnectResponseSchema.parse(data).authorize_url
}

// GET /api/v1/integrations/freeagent/expenses/:id/push — the push outcome for one
// expense (the detail-page badge). Owner/admin only.
export async function getExpensePushStatus(expenseId: string): Promise<FreeAgentPushStatus> {
  const data = await apiFetch<unknown>(
    `/integrations/freeagent/expenses/${encodeURIComponent(expenseId)}/push`,
    { method: 'GET' },
  )
  return GetFreeAgentPushStatusResponseSchema.parse(data).push
}

// POST /api/v1/integrations/freeagent/expenses/:id/push — manually (re-)push an
// APPROVED expense. 202 Accepted (no body); the workflow's already_pushed guard
// makes it idempotent. Owner/admin only.
export async function repushExpense(expenseId: string): Promise<void> {
  await apiFetch<unknown>(
    `/integrations/freeagent/expenses/${encodeURIComponent(expenseId)}/push`,
    { method: 'POST' },
  )
}

// =============================================================================
// HMRC Making Tax Digital
// =============================================================================

// GET /api/v1/integrations/hmrc — the org's HMRC MTD connection status.
// Owner/admin only. Identical structure to getFreeAgentStatus.
export async function getHmrcStatus(): Promise<HmrcStatus> {
  const data = await apiFetch<unknown>('/integrations/hmrc', { method: 'GET' })
  return GetHmrcStatusResponseSchema.parse(data).integration
}

// DELETE /api/v1/integrations/hmrc — disconnect this org's HMRC tokens.
// Owner/admin only. Idempotent.
export async function disconnectHmrc(): Promise<void> {
  await apiFetch<unknown>('/integrations/hmrc', { method: 'DELETE' })
}

// GET /api/v1/hmrc/connect — the HMRC authorize URL to navigate to.
// Owner/admin only; a 422 means HMRC isn't configured (no app credentials in DB).
export async function getHmrcConnectUrl(): Promise<string> {
  const data = await apiFetch<unknown>('/hmrc/connect', { method: 'GET' })
  return ConnectResponseSchema.parse(data).authorize_url
}
