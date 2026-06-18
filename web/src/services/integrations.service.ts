import { apiFetch } from '@/lib/api'
import {
  GetFreeAgentStatusResponseSchema,
  ConnectResponseSchema,
  GetFreeAgentPushStatusResponseSchema,
  type FreeAgentStatus,
  type FreeAgentPushStatus,
  type SaveFreeAgentCredentialsRequest,
} from '@/types/integration'

// GET /api/v1/integrations/freeagent — the org's FreeAgent connection status. The
// bearer token is attached by apiFetch; a 401 is handled there. OWNER/ADMIN only on
// the backend, so a 403 (member/non-member) surfaces as an ApiError. No secrets.
export async function getFreeAgentStatus(): Promise<FreeAgentStatus> {
  const data = await apiFetch<unknown>('/integrations/freeagent', { method: 'GET' })
  return GetFreeAgentStatusResponseSchema.parse(data).integration
}

// PUT /api/v1/integrations/freeagent — save (or replace) the OAuth app credentials.
// Returns the updated status ("credentials saved, not yet connected"). Owner/admin
// only; a 400 (missing field) / 403 surfaces as an ApiError for the form to display.
export async function saveFreeAgentCredentials(
  payload: SaveFreeAgentCredentialsRequest,
): Promise<FreeAgentStatus> {
  const data = await apiFetch<unknown>('/integrations/freeagent', { method: 'PUT', body: payload })
  return GetFreeAgentStatusResponseSchema.parse(data).integration
}

// DELETE /api/v1/integrations/freeagent — disconnect: drop the tokens but KEEP the
// credentials (so reconnecting is one click). 204, no body — apiFetch tolerates the
// empty response. Owner/admin only.
export async function disconnectFreeAgent(): Promise<void> {
  await apiFetch<unknown>('/integrations/freeagent', { method: 'DELETE' })
}

// GET /api/v1/freeagent/connect — the FreeAgent authorize URL the SPA navigates to.
// Owner/admin only; a 422 means "save your credentials first".
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
