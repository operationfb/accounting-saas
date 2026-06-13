import { z } from 'zod'

// Mirrors the backend's ExpenseResponse (server.go). Money fields are decimal
// POUND strings (e.g. "42.50"), not pence — formatted for display, never used
// for client-side arithmetic.
export const ExpenseSchema = z.object({
  id: z.string(),
  organisation_id: z.string(),
  user_id: z.string(),
  category_id: z.string(),
  dated_on: z.string(), // YYYY-MM-DD
  description: z.string(),
  currency: z.string(),
  gross_value: z.string(),
  native_gross_value: z.string(),
  vat_value: z.string(),
  status: z.string(),
  receipt_reference: z.string().nullish(),
  supplier_name: z.string().nullish(),
  supplier_vat_number: z.string().nullish(),
  invoice_number: z.string().nullish(),
  created_at: z.string(),
  updated_at: z.string(),
})
export type Expense = z.infer<typeof ExpenseSchema>

// GET /api/v1/expenses → { "expenses": [...] }. An empty list can come back as
// null (Go marshals a nil slice to null), so allow null and default to [].
export const ListExpensesResponseSchema = z.object({
  expenses: z.array(ExpenseSchema).nullish(),
})

// GET /api/v1/expenses/:id returns the RICH detail (from v_expenses_full) — a
// superset of the lean list item with category name, VAT rate/status, FX, etc.
export const ExpenseDetailSchema = z.object({
  id: z.string(),
  status: z.string(),
  dated_on: z.string(),
  description: z.string(),
  category_name: z.string(),
  category_nominal_code: z.string(),
  currency: z.string(),
  gross_value: z.string(),
  vat_rate: z.string().nullish(),
  vat_status: z.string(),
  vat_value: z.string(),
  native_currency: z.string(),
  native_gross_value: z.string(),
  native_vat_value: z.string(),
  exchange_rate: z.string().nullish(),
  ec_status: z.string(),
  supplier_name: z.string().nullish(),
  supplier_vat_number: z.string().nullish(),
  invoice_number: z.string().nullish(),
  receipt_reference: z.string().nullish(),
  project_id: z.string().nullish(),
  rebill_type: z.string().nullish(),
  rebill_factor: z.string().nullish(),
  submitted_at: z.string().nullish(),
  approved_at: z.string().nullish(),
  paid_at: z.string().nullish(),
  created_at: z.string(),
  updated_at: z.string(),
})
export type ExpenseDetail = z.infer<typeof ExpenseDetailSchema>

// GET /api/v1/expenses/:id → { "expense": {...} }.
export const GetExpenseResponseSchema = z.object({
  expense: ExpenseDetailSchema,
})
