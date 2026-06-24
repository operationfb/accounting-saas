import { apiFetch } from '@/lib/api'
import {
  GetVatSettingsResponseSchema,
  type VatSettings,
  type VatSettingsRequest,
} from '@/types/vat'

// GET /api/v1/vat/settings — the caller's own VAT registration settings. Any
// ACTIVE member may read; the organisation is taken from the bearer token (no id
// in the path), so a caller can only ever read their own org. apiFetch attaches
// the token and handles a 401; a 403 (inactive/non-member) surfaces as an ApiError.
export async function getVatSettings(): Promise<VatSettings> {
  const data = await apiFetch<unknown>('/vat/settings', { method: 'GET' })
  return GetVatSettingsResponseSchema.parse(data).vat_settings
}

// PUT /api/v1/vat/settings — update the VAT settings. OWNER/ADMIN only on the
// backend; the UI also hides Save for everyone else. Returns the updated record.
// A 403 (not owner/admin) or 422 (bad value, e.g. a malformed VRN, or a missing
// required field while registered) surfaces as an ApiError for the form to show.
export async function updateVatSettings(payload: VatSettingsRequest): Promise<VatSettings> {
  const data = await apiFetch<unknown>('/vat/settings', { method: 'PUT', body: payload })
  return GetVatSettingsResponseSchema.parse(data).vat_settings
}
