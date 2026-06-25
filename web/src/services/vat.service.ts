import { apiFetch } from '@/lib/api'
import {
  GetVatSettingsResponseSchema,
  ListVatPeriodsResponseSchema,
  GetVatReturnResponseSchema,
  type VatSettings,
  type VatSettingsRequest,
  type VatPeriod,
  type VatReturn,
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

// GET /api/v1/vat/periods — the org's VAT return periods, generated from its
// settings (newest-first). Any ACTIVE member may read. Empty when the org isn't
// VAT-registered or its settings are incomplete.
export async function listVatPeriods(): Promise<VatPeriod[]> {
  const data = await apiFetch<unknown>('/vat/periods', { method: 'GET' })
  return ListVatPeriodsResponseSchema.parse(data).periods ?? []
}

// GET /api/v1/vat/returns/:periodKey — the computed return (9 boxes + the
// contributing lines) for one period. Any ACTIVE member may read. An unknown
// period (or a not-registered org) is a 404 ApiError.
export async function getVatReturn(periodKey: string): Promise<VatReturn> {
  const data = await apiFetch<unknown>(`/vat/returns/${encodeURIComponent(periodKey)}`, {
    method: 'GET',
  })
  return GetVatReturnResponseSchema.parse(data).vat_return
}

// POST /api/v1/vat/returns/:periodKey/mark-filed — snapshot the return and mark it
// filed, which LOCKS the period (records dated inside it can no longer be changed).
// OWNER/ADMIN only on the backend (403 otherwise).
export async function markVatReturnFiled(periodKey: string): Promise<VatReturn> {
  const data = await apiFetch<unknown>(
    `/vat/returns/${encodeURIComponent(periodKey)}/mark-filed`,
    { method: 'POST' },
  )
  return GetVatReturnResponseSchema.parse(data).vat_return
}
