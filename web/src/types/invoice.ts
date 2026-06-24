import { z } from 'zod'

// An invoice + its line items as returned by the invoices API. Mirrors the Go
// InvoiceResponse / InvoiceItemResponse (internal/invoices/dto.go): money arrives
// as decimal POUND strings (e.g. "1200.00") — never do arithmetic on them, just
// format for display — VAT rates as PERCENTAGE strings (e.g. "20"), dates as
// "YYYY-MM-DD", timestamps as RFC3339. Nullable fields use .nullish().

// One line. net_value / sales_tax_value / total_value are the backend-computed
// per-line amounts (we display them; we don't recompute). price is the per-unit,
// VAT-exclusive price in pounds.
export const InvoiceItemSchema = z.object({
  id: z.string(),
  position: z.number(),
  description: z.string(),
  quantity: z.string(),
  price: z.string(), // per-unit, pounds, VAT-exclusive
  sales_tax_rate: z.string(), // percent, e.g. "20"
  net_value: z.string(),
  sales_tax_value: z.string(),
  total_value: z.string(),
})
export type InvoiceItem = z.infer<typeof InvoiceItemSchema>

// The invoice header. `status` is the stored lifecycle (DRAFT | SCHEDULED | SENT |
// WRITTEN_OFF | REFUNDED); `display_status` is the derived presentation (Draft |
// Open | Overdue | Paid | Overpaid | Zero Value | Scheduled | Written off |
// Refunded). `items` is present on the detail responses (get/create/update),
// omitted from the list (so .nullish()).
export const InvoiceSchema = z.object({
  id: z.string(),
  organisation_id: z.string(),
  created_by_user_id: z.string(),
  contact_id: z.string(),

  dated_on: z.string(),
  due_on: z.string().nullish(),
  reference: z.string().nullish(),
  currency: z.string(),

  status: z.string(),
  display_status: z.string(),

  net_value: z.string(),
  sales_tax_value: z.string(),
  total_value: z.string(),
  paid_value: z.string(),
  due_value: z.string(),

  items: z.array(InvoiceItemSchema).nullish(),

  created_at: z.string(),
  updated_at: z.string(),
})
export type Invoice = z.infer<typeof InvoiceSchema>

// GET /api/v1/invoices → { invoices: [...] }. An empty list may arrive as null
// (Go marshals a nil slice to null), so the service defaults it to [].
export const ListInvoicesResponseSchema = z.object({
  invoices: z.array(InvoiceSchema).nullish(),
})

// GET /:id, POST, PUT and POST /:id/status all return a single invoice as
// { invoice: {...} }.
export const GetInvoiceResponseSchema = z.object({
  invoice: InvoiceSchema,
})

// One line on a create/update payload (Go InvoiceItemRequest). price is per-unit
// pounds (VAT-exclusive); quantity a decimal string; sales_tax_rate a percentage
// string ("20", "0").
export interface InvoiceItemRequest {
  description: string
  quantity: string
  price: string
  sales_tax_rate: string
}

// Body for POST /api/v1/invoices (create) and PUT /api/v1/invoices/:id (update) —
// the two share the same shape (Go CreateInvoiceRequest). Only contact_id +
// dated_on are required.
//
// IMPORTANT: the backend PUT REBUILDS all line items from `items`, so an update
// must always carry the FULL current list — never a partial one — or the omitted
// lines are deleted.
export interface CreateInvoiceRequest {
  contact_id: string
  dated_on: string // "YYYY-MM-DD"
  due_on?: string // "YYYY-MM-DD"
  reference: string // required (auto-numbered, user-overridable)
  currency?: string
  items: InvoiceItemRequest[]
}
