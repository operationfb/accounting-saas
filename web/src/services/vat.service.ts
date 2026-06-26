import { apiFetch } from '@/lib/api'
import {
  GetVatSettingsResponseSchema,
  ListVatPeriodsResponseSchema,
  GetVatReturnResponseSchema,
  SubmitVatReturnResponseSchema,
  GetVatPeriodCheckResponseSchema,
  ListHmrcObligationsResponseSchema,
  GetHmrcReturnResponseSchema,
  ListHmrcLiabilitiesResponseSchema,
  ListHmrcPaymentsResponseSchema,
  GetHmrcPenaltiesResponseSchema,
  GetHmrcFinancialDetailsResponseSchema,
  GetHmrcInformationResponseSchema,
  type VatSettings,
  type VatSettingsRequest,
  type VatPeriod,
  type VatReturn,
  type VatSubmitResponse,
  type VatPeriodCheck,
  type HmrcObligation,
  type HmrcReturn,
  type HmrcLiability,
  type HmrcPayment,
  type HmrcPenalties,
  type HmrcFinancialDetails,
  type HmrcInformation,
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

// POST /api/v1/vat/returns/:periodKey/submit — submit the return to HMRC via
// Making Tax Digital. OWNER/ADMIN only; requires an active HMRC connection (409
// if not connected). Returns the HMRC acknowledgement (form bundle number +
// processing date) — NOT a VatReturn, so the caller should re-fetch the return
// afterwards to pick up the now-"Filed" status and period lock. Errors map to
// ApiError: 409 (not connected / duplicate / period already filed), 422 (no VRN,
// period not ended, HMRC validation), 404 (unknown period).
export async function submitVatReturn(periodKey: string): Promise<VatSubmitResponse> {
  const data = await apiFetch<unknown>(
    `/vat/returns/${encodeURIComponent(periodKey)}/submit`,
    { method: 'POST', fraudHeaders: true },
  )
  return SubmitVatReturnResponseSchema.parse(data).submission
}

// GET /api/v1/vat/hmrc/period-check — does the org's generated VAT period schedule
// line up with HMRC's obligations? Drives the post-connect reconciliation modal.
// OWNER/ADMIN only; fails open (applicable=false) when there's nothing to compare.
export async function checkHmrcPeriods(): Promise<VatPeriodCheck> {
  const data = await apiFetch<unknown>('/vat/hmrc/period-check', { method: 'GET', fraudHeaders: true })
  return GetVatPeriodCheckResponseSchema.parse(data).period_check
}

// POST /api/v1/vat/hmrc/period-sync — rewrite the org's VAT period settings to
// match HMRC's obligations (the modal's "Adjust to match HMRC" action). OWNER/ADMIN
// only. Returns the updated settings.
export async function syncHmrcPeriods(): Promise<VatSettings> {
  const data = await apiFetch<unknown>('/vat/hmrc/period-sync', { method: 'POST', fraudHeaders: true })
  return GetVatSettingsResponseSchema.parse(data).vat_settings
}

// =============================================================================
// VAT dashboard — the read layer over HMRC's MTD VAT account. Each call hits HMRC
// live (the data isn't stored on our side); any ACTIVE member may read. A VRN must
// be set (422 otherwise) and HMRC must be connected (409 otherwise).
// =============================================================================

// GET /api/v1/vat/hmrc/obligations — the org's VAT return periods from HMRC.
// Optional status filter: "O" (open) or "F" (fulfilled).
export async function getHmrcObligations(status?: 'O' | 'F'): Promise<HmrcObligation[]> {
  const qs = status ? `?status=${status}` : ''
  const data = await apiFetch<unknown>(`/vat/hmrc/obligations${qs}`, { method: 'GET', fraudHeaders: true })
  return ListHmrcObligationsResponseSchema.parse(data).obligations ?? []
}

// GET /api/v1/vat/hmrc/returns/:periodKey — HMRC's view of a submitted return.
export async function getHmrcReturn(periodKey: string): Promise<HmrcReturn> {
  const data = await apiFetch<unknown>(`/vat/hmrc/returns/${encodeURIComponent(periodKey)}`, {
    method: 'GET',
    fraudHeaders: true,
  })
  return GetHmrcReturnResponseSchema.parse(data).hmrc_return
}

// GET /api/v1/vat/hmrc/liabilities — amounts owed to HMRC ("what you owe").
export async function getHmrcLiabilities(): Promise<HmrcLiability[]> {
  const data = await apiFetch<unknown>('/vat/hmrc/liabilities', { method: 'GET', fraudHeaders: true })
  return ListHmrcLiabilitiesResponseSchema.parse(data).liabilities ?? []
}

// GET /api/v1/vat/hmrc/payments — payments received by HMRC.
export async function getHmrcPayments(): Promise<HmrcPayment[]> {
  const data = await apiFetch<unknown>('/vat/hmrc/payments', { method: 'GET', fraudHeaders: true })
  return ListHmrcPaymentsResponseSchema.parse(data).payments ?? []
}

// GET /api/v1/vat/hmrc/penalties — late-submission points + penalty charges.
export async function getHmrcPenalties(): Promise<HmrcPenalties> {
  const data = await apiFetch<unknown>('/vat/hmrc/penalties', { method: 'GET', fraudHeaders: true })
  return GetHmrcPenaltiesResponseSchema.parse(data).penalties
}

// GET /api/v1/vat/hmrc/financial-details/:chargeRef — one penalty's charge breakdown.
export async function getHmrcFinancialDetails(chargeRef: string): Promise<HmrcFinancialDetails> {
  const data = await apiFetch<unknown>(
    `/vat/hmrc/financial-details/${encodeURIComponent(chargeRef)}`,
    { method: 'GET', fraudHeaders: true },
  )
  return GetHmrcFinancialDetailsResponseSchema.parse(data).financial_details
}

// GET /api/v1/vat/hmrc/information — the registered VAT business details.
export async function getHmrcInformation(): Promise<HmrcInformation> {
  const data = await apiFetch<unknown>('/vat/hmrc/information', { method: 'GET', fraudHeaders: true })
  return GetHmrcInformationResponseSchema.parse(data).information
}
