import { z } from 'zod'

// Mirrors the backend's VatSettingsResponse (internal/vat/dto.go) — the org's
// "UK VAT Registration" settings, modelled on FreeAgent. A SINGLETON resource:
// the org comes from the bearer token, so there is no list and no id in the URL.
//
// `vat_registered`, `uses_non_standard_rates` and `flat_rate_scheme` always come
// back (plain booleans). Everything else is nullish — a not-yet-registered org
// has none of it.
export const VatSettingsSchema = z.object({
  vat_registered: z.boolean(),
  vrn: z.string().nullish(), // bare 9 digits, no GB prefix
  uses_non_standard_rates: z.boolean(),
  effective_date: z.string().nullish(), // YYYY-MM-DD
  first_return_period_end: z.string().nullish(), // YYYY-MM-DD
  return_frequency: z.string().nullish(), // monthly | quarterly | annually
  accounting_basis: z.string().nullish(), // invoice | cash
  flat_rate_scheme: z.boolean(),
  flat_rate_percentage: z.string().nullish(), // e.g. "10.5"
  pre_reg_expense_months: z.number().nullish(), // 6 | 48 | null
  // HMRC Making Tax Digital connection status — derived live from the
  // integrations table by the backend. Drives the "Submit to HMRC" button.
  hmrc_connected: z.boolean().nullish(),
  hmrc_connected_at: z.string().nullish(), // RFC3339
})
export type VatSettings = z.infer<typeof VatSettingsSchema>

// GET /api/v1/vat/settings and PUT both return { "vat_settings": {...} }.
export const GetVatSettingsResponseSchema = z.object({
  vat_settings: VatSettingsSchema,
})

// --- VAT return periods (GET /api/v1/vat/periods) ---

// Mirrors the backend's VatPeriodResponse (internal/vat/dto.go). A generated return
// period: `period_key` is the synthetic id (the period-end date) used to address
// the return; `label` is "MM YY" of the period end (e.g. "05 26"); `display_status`
// is "Open" (in progress) or "Unfiled" (ended) in v1.
export const VatPeriodSchema = z.object({
  period_key: z.string(),
  label: z.string(),
  start_date: z.string(),
  end_date: z.string(),
  due_on: z.string(),
  ended: z.boolean(),
  display_status: z.string(),
})
export type VatPeriod = z.infer<typeof VatPeriodSchema>

// GET /api/v1/vat/periods returns { "periods": [...] } (possibly null/empty).
export const ListVatPeriodsResponseSchema = z.object({
  periods: z.array(VatPeriodSchema).nullish(),
})

// --- the computed VAT return (GET /api/v1/vat/returns/:periodKey) ---

// One contributing transaction in the Full Report. Money is exact 2dp pound strings.
// `id` is the underlying record's UUID — present for expense/invoice/bill lines (so
// the row can link to its detail view), absent for bank/cash lines.
export const VatReturnLineSchema = z.object({
  id: z.string().nullish(),
  date: z.string(),
  source: z.string(), // invoice | expense | bill | bank
  description: z.string(),
  reference: z.string().nullish(),
  net: z.string(),
  vat: z.string(),
})
export type VatReturnLine = z.infer<typeof VatReturnLineSchema>

// Mirrors the backend's VatReturnResponse (internal/vat/dto.go). Boxes 1–5 are VAT
// amounts (2dp); boxes 6–9 are net values rounded to whole pounds. `net_due` is the
// signed Box 5 (negative = a reclaim/refund), `is_reclaim` the sign as a bool.
export const VatReturnSchema = z.object({
  period_key: z.string(),
  label: z.string(),
  start_date: z.string(),
  end_date: z.string(),
  due_on: z.string(),
  display_status: z.string(),
  accounting_basis: z.string(), // invoice | cash
  box1_vat_due_sales: z.string(),
  box2_vat_due_acquisitions: z.string(),
  box3_total_vat_due: z.string(),
  box4_vat_reclaimed: z.string(),
  box5_net_vat: z.string(),
  box6_total_sales_ex_vat: z.string(),
  box7_total_purchases_ex_vat: z.string(),
  box8_ec_dispatches_ex_vat: z.string(),
  box9_ec_acquisitions_ex_vat: z.string(),
  net_due: z.string(),
  is_reclaim: z.boolean(),
  sales_lines: z.array(VatReturnLineSchema).nullish(),
  purchase_lines: z.array(VatReturnLineSchema).nullish(),
})
export type VatReturn = z.infer<typeof VatReturnSchema>

// GET /api/v1/vat/returns/:periodKey returns { "vat_return": {...} }.
export const GetVatReturnResponseSchema = z.object({
  vat_return: VatReturnSchema,
})

// --- HMRC online submission (POST /api/v1/vat/returns/:periodKey/submit) ---

// Mirrors the backend's VatSubmitResponse (internal/vat/dto.go) — the HMRC
// acknowledgement after a successful MTD submission. `form_bundle_number` is the
// receipt the taxpayer keeps; `charge_ref_number` is present only when HMRC
// raises a payment charge (absent for a nil/repayment return).
export const VatSubmitResponseSchema = z.object({
  period_key: z.string(),
  form_bundle_number: z.string(),
  processing_date: z.string(), // ISO8601
  charge_ref_number: z.string().nullish(),
})
export type VatSubmitResponse = z.infer<typeof VatSubmitResponseSchema>

// POST .../submit returns { "submission": {...} }.
export const SubmitVatReturnResponseSchema = z.object({
  submission: VatSubmitResponseSchema,
})

// --- HMRC period reconciliation (GET /api/v1/vat/hmrc/period-check) ---

// The (frequency, first-period-end, effective-date) triple that drives the
// generated VAT period schedule. Dates are YYYY-MM-DD; fields may be empty when
// the current settings are incomplete.
export const VatPeriodSettingsSchema = z.object({
  return_frequency: z.string().nullish(),
  first_return_period_end: z.string().nullish(),
  effective_date: z.string().nullish(),
})
export type VatPeriodSettings = z.infer<typeof VatPeriodSettingsSchema>

// Mirrors the backend's VatPeriodCheckResponse (internal/vat/dto.go). `applicable`
// is false when there's nothing to reconcile (not connected, no VRN, no
// obligations, HMRC error — fail open). When `applicable && !matches`, the SPA
// offers to adjust settings to `suggested`. `filed_periods_affected` warns how
// many saved returns would drop off the list after the rewrite.
export const VatPeriodCheckSchema = z.object({
  applicable: z.boolean(),
  matches: z.boolean(),
  current: VatPeriodSettingsSchema,
  suggested: VatPeriodSettingsSchema,
  filed_periods_affected: z.number(),
})
export type VatPeriodCheck = z.infer<typeof VatPeriodCheckSchema>

// GET /api/v1/vat/hmrc/period-check returns { "period_check": {...} }.
export const GetVatPeriodCheckResponseSchema = z.object({
  period_check: VatPeriodCheckSchema,
})

// PUT body — mirrors the backend's VatSettingsRequest. `vat_registered` is the
// master switch; when true the certificate fields (vrn, the two dates, frequency,
// accounting basis) are required — the backend enforces this too. Empty optionals
// are omitted; pre_reg_expense_months sends null for "don't include".
export interface VatSettingsRequest {
  vat_registered: boolean
  vrn?: string
  uses_non_standard_rates: boolean
  effective_date?: string
  first_return_period_end?: string
  return_frequency?: string
  accounting_basis?: string
  flat_rate_scheme: boolean
  flat_rate_percentage?: string
  pre_reg_expense_months?: number | null
}

// --- dropdown options (codes mirror the backend enums + the DB CHECKs) ---

// "Are you VAT Registered?" — a simple yes/no stored as a boolean.
export const VAT_REGISTERED_OPTIONS: { label: string; value: boolean }[] = [
  { label: 'Registered', value: true },
  { label: 'Not Registered', value: false },
]

export const RETURN_FREQUENCY_OPTIONS: { label: string; value: string }[] = [
  { label: 'Monthly', value: 'monthly' },
  { label: 'Quarterly', value: 'quarterly' },
  { label: 'Annually', value: 'annually' },
]

export const ACCOUNTING_BASIS_OPTIONS: { label: string; value: string }[] = [
  { label: 'Invoice', value: 'invoice' },
  { label: 'Cash', value: 'cash' },
]

// "Include pre-registration expenses from" — stored as a month count (null = don't
// include; 6 months for services, 4 years/48 months for goods — HMRC's rules).
export const PRE_REG_OPTIONS: { label: string; value: number | null }[] = [
  { label: "Don't include pre-registration expenses", value: null },
  { label: 'From the last 6 months', value: 6 },
  { label: 'From the last 4 years', value: 48 },
]

// =============================================================================
// HMRC VAT-account dashboard (the read layer over the MTD VAT GET APIs). These
// mirror the backend's HMRC* DTOs (internal/vat/dto.go): money is 2dp pound
// STRINGS, dates are YYYY-MM-DD strings, optional fields are nullish.
// =============================================================================

// One VAT obligation (a return period). status is "O" (open) or "F" (fulfilled);
// received is the filed date, present only when fulfilled.
export const HmrcObligationSchema = z.object({
  period_key: z.string(),
  start: z.string(),
  end: z.string(),
  due: z.string(),
  status: z.string(),
  received: z.string().nullish(),
})
export type HmrcObligation = z.infer<typeof HmrcObligationSchema>
export const ListHmrcObligationsResponseSchema = z.object({
  obligations: z.array(HmrcObligationSchema).nullish(),
})

// HMRC's view of a submitted return (the 9 boxes).
export const HmrcReturnSchema = z.object({
  period_key: z.string(),
  box1_vat_due_sales: z.string(),
  box2_vat_due_acquisitions: z.string(),
  box3_total_vat_due: z.string(),
  box4_vat_reclaimed: z.string(),
  box5_net_vat: z.string(),
  box6_total_sales_ex_vat: z.string(),
  box7_total_purchases_ex_vat: z.string(),
  box8_ec_dispatches_ex_vat: z.string(),
  box9_ec_acquisitions_ex_vat: z.string(),
})
export type HmrcReturn = z.infer<typeof HmrcReturnSchema>
export const GetHmrcReturnResponseSchema = z.object({ hmrc_return: HmrcReturnSchema })

// One amount owed to HMRC ("what you owe").
export const HmrcLiabilitySchema = z.object({
  type: z.string(),
  from: z.string().nullish(),
  to: z.string().nullish(),
  original_amount: z.string(),
  outstanding_amount: z.string(),
  due: z.string().nullish(),
})
export type HmrcLiability = z.infer<typeof HmrcLiabilitySchema>
export const ListHmrcLiabilitiesResponseSchema = z.object({
  liabilities: z.array(HmrcLiabilitySchema).nullish(),
})

// One payment received by HMRC.
export const HmrcPaymentSchema = z.object({
  amount: z.string(),
  received: z.string().nullish(),
})
export type HmrcPayment = z.infer<typeof HmrcPaymentSchema>
export const ListHmrcPaymentsResponseSchema = z.object({
  payments: z.array(HmrcPaymentSchema).nullish(),
})

// One penalty charge (late submission or late payment); charge_reference drills
// into financial-details.
export const HmrcPenaltyChargeSchema = z.object({
  type: z.string(),
  category: z.string().nullish(),
  charge_reference: z.string().nullish(),
  status: z.string().nullish(),
  amount: z.string(),
})
export type HmrcPenaltyCharge = z.infer<typeof HmrcPenaltyChargeSchema>

// The penalties summary: the late-submission points meter + the charges.
export const HmrcPenaltiesSchema = z.object({
  active_points: z.number(),
  inactive_points: z.number(),
  threshold: z.number(),
  total_penalties: z.string(),
  penalties: z.array(HmrcPenaltyChargeSchema).nullish(),
})
export type HmrcPenalties = z.infer<typeof HmrcPenaltiesSchema>
export const GetHmrcPenaltiesResponseSchema = z.object({ penalties: HmrcPenaltiesSchema })

// The charge breakdown for one penalty (financial-details drill-down).
export const HmrcFinancialDocSchema = z.object({
  type: z.string().nullish(),
  charge_reference: z.string().nullish(),
  total_amount: z.string(),
  outstanding_amount: z.string(),
  due_date: z.string().nullish(),
})
export const HmrcFinancialDetailsSchema = z.object({
  charge_reference: z.string(),
  documents: z.array(HmrcFinancialDocSchema).nullish(),
})
export type HmrcFinancialDetails = z.infer<typeof HmrcFinancialDetailsSchema>
export const GetHmrcFinancialDetailsResponseSchema = z.object({
  financial_details: HmrcFinancialDetailsSchema,
})

// Registered VAT business details.
export const HmrcInformationSchema = z.object({
  business_name: z.string().nullish(),
  trading_name: z.string().nullish(),
  address_lines: z.array(z.string()).nullish(),
  postcode: z.string().nullish(),
  country_code: z.string().nullish(),
  registration_date: z.string().nullish(),
})
export type HmrcInformation = z.infer<typeof HmrcInformationSchema>
export const GetHmrcInformationResponseSchema = z.object({ information: HmrcInformationSchema })
