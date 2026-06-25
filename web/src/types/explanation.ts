import { z } from 'zod'

// Types for the explain/reconcile flow. Mirrors the backend DTOs in
// internal/banking/explain.go + internal/categories. Money crosses as pound strings.

// GET /api/v1/transaction-types → { transaction_types: [...] } — the explain "Type"
// dropdown. supported=false marks a future-entity type (Bill/Invoice/Credit Note/HP)
// that isn't explainable yet (the UI disables it).
export const TransactionTypeSchema = z.object({
  code: z.string(),
  name: z.string(),
  direction: z.string(), // 'in' | 'out'
  entity_link: z.string(), // NONE | BANK_ACCOUNT | USER | CAPITAL_ASSET | …
  supported: z.boolean(),
})
export type TransactionType = z.infer<typeof TransactionTypeSchema>

export const ListTransactionTypesResponseSchema = z.object({
  transaction_types: z.array(TransactionTypeSchema).nullish(),
})

// GET /api/v1/transaction-types/:code/categories → { categories: [...] } — the CoA
// accounts a type offers. name is the offered label; default_vat pre-selects the rate.
export const ExplanationCategorySchema = z.object({
  id: z.string(),
  nominal_code: z.string(),
  name: z.string(),
  account_type: z.string(),
  api_group: z.string().nullish(),
  default_vat: z.string().nullish(), // STANDARD | ZERO | EXEMPT | …
})
export type ExplanationCategory = z.infer<typeof ExplanationCategorySchema>

export const ListCategoriesForTypeResponseSchema = z.object({
  categories: z.array(ExplanationCategorySchema).nullish(),
})

// One explanation (a whole line or a split portion). amount + vat_value are positive
// pound strings (the direction is the line's).
export const ExplanationSchema = z.object({
  id: z.string(),
  type: z.string(),
  amount: z.string(),
  category_id: z.string().nullish(),
  category_name: z.string().nullish(),
  category_nominal_code: z.string().nullish(),
  transfer_bank_account_id: z.string().nullish(),
  transfer_account_name: z.string().nullish(),
  paid_user_id: z.string().nullish(),
  paid_user_name: z.string().nullish(),
  paid_invoice_id: z.string().nullish(),
  invoice_reference: z.string().nullish(), // the settled invoice's number, for display
  paid_bill_id: z.string().nullish(),
  bill_reference: z.string().nullish(), // the settled bill's reference, for display
  vat_rate_id: z.string().nullish(),
  vat_rate: z.string().nullish(), // "20%"
  vat_value: z.string(),
  description: z.string().nullish(),
  dated_on: z.string(),
  marked_for_review: z.boolean(),
})
export type Explanation = z.infer<typeof ExplanationSchema>

// GET + every mutation → the line's recomputed reconcile state + its explanations.
// unexplained_amount is the signed pounds remaining to explain (the UI abs()es it).
export const TransactionExplanationsResponseSchema = z.object({
  transaction_id: z.string(),
  status: z.string(), // unexplained | explained | for_approval
  unexplained_amount: z.string(),
  explanations: z.array(ExplanationSchema).nullish(),
})
export type TransactionExplanations = z.infer<typeof TransactionExplanationsResponseSchema>

// POST/PUT body. amount is a POSITIVE pound string; which of category/transfer/user
// is required depends on the chosen type.
export interface CreateExplanationRequest {
  type: string
  amount: string
  category_id?: string
  transfer_bank_account_id?: string
  paid_user_id?: string
  paid_invoice_id?: string // invoice receipts
  paid_bill_id?: string // bill payments
  vat_rate_id?: string
  vat_amount?: string // manual (non-fixed) rate only
  description?: string
  dated_on?: string
}
