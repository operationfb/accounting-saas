import { apiFetch } from '@/lib/api'
import type { TalaChatMessage, TalaChatResponse } from '@/types/tala'

// POST /api/v1/tala/chat — send the running conversation and get Tala's reply,
// plus any guarded-write proposals for the user to confirm. The bearer token and
// the 401 → logout/redirect handling come from apiFetch.
export async function sendTalaChat(messages: TalaChatMessage[]): Promise<TalaChatResponse> {
  return apiFetch<TalaChatResponse>('/tala/chat', { method: 'POST', body: { messages } })
}
