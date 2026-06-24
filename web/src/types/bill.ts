import { z } from 'zod'

// A bill (an accounts-PAYABLE supplier invoice) as returned by the bills API.
// Mirrors the Go BillResponse (internal/bills/dto.go): money arrives as decimal
// POUND strings (e.g. "120.00") — never do arithmetic on them, just format for
// display — dates as "YYYY-MM-DD", timestamps as RFC3339. Nullable fields use
// .nullish().
//
// A bill is a SINGLE flat record (like an expense): one spending category, a
// VAT-INCLUSIVE total, and an optional VAT rate. There is NO status lifecycle;
// `display_status` (Unpaid | Part paid | Paid | Overdue | Zero Value) is DERIVED by
// the backend from paid/total/due_on. A bill is editable/deletable only while
// UNPAID (paid_value "0.00"); the banking module owns paid_value.
export const BillSchema = z.object({
  id: z.string(),
  organisation_id: z.string(),
  created_by_user_id: z.string(),
  contact_id: z.string(),

  reference: z.string().nullish(),
  dated_on: z.string(),
  due_on: z.string().nullish(),
  currency: z.string(),
  comments: z.string().nullish(),
  is_hire_purchase: z.boolean(),

  category_id: z.string(),
  vat_rate_id: z.string().nullish(),
  vat_rate: z.string(), // display percent, e.g. "20%" ("" = no rate selected)

  net_value: z.string(),
  sales_tax_value: z.string(),
  total_value: z.string(),
  paid_value: z.string(),
  due_value: z.string(),

  display_status: z.string(),

  project_id: z.string().nullish(),

  created_at: z.string(),
  updated_at: z.string(),
})
export type Bill = z.infer<typeof BillSchema>

// GET /api/v1/bills → { bills: [...] }. An empty list may arrive as null (Go
// marshals a nil slice to null), so the service defaults it to [].
export const ListBillsResponseSchema = z.object({
  bills: z.array(BillSchema).nullish(),
})

// GET /:id, POST and PUT all return a single bill as { bill: {...} }.
export const GetBillResponseSchema = z.object({
  bill: BillSchema,
})

// One row of the "Spending Category" picker (GET /api/v1/bill-categories). Mirrors
// the Go BillCategoryResponse — the spending subset of the Chart of Accounts.
// default_vat lets the SPA pre-select a sensible VAT rate (not used yet).
export const BillCategorySchema = z.object({
  id: z.string(),
  nominal_code: z.string(),
  name: z.string(),
  account_type: z.string(), // COST_OF_SALES | ADMIN_EXPENSE | CAPITAL_ASSET
  api_group: z.string().nullish(),
  default_vat: z.string().nullish(),
})
export type BillCategory = z.infer<typeof BillCategorySchema>

export const ListBillCategoriesResponseSchema = z.object({
  bill_categories: z.array(BillCategorySchema).nullish(),
})

// Body for POST /api/v1/bills (create) and PUT /api/v1/bills/:id (update) — the two
// share the same shape (Go CreateBillRequest). contact_id, reference, dated_on,
// category_id and total are required. VAT mirrors the expenses pattern: vat_rate_id
// picks a vat_rates row; for a non-fixed-ratio ("manual") rate, vat_amount carries
// the VAT. `total` is the VAT-INCLUSIVE amount (may be negative — a bill credit note).
export interface CreateBillRequest {
  contact_id: string
  reference: string
  dated_on: string // "YYYY-MM-DD"
  due_on?: string // "YYYY-MM-DD"
  currency?: string
  comments?: string
  is_hire_purchase: boolean
  category_id: string
  total: string // VAT-inclusive pounds
  vat_rate_id?: string
  vat_amount?: string // pounds; only used for a non-fixed-ratio (manual) rate
  project_id?: string
}
