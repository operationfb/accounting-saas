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
export const VatReturnLineSchema = z.object({
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
