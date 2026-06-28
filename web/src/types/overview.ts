import { z } from 'zod'

// Overview dashboard data as returned by the Overview API. Mirrors the Go DTOs
// (internal/overview/dto.go): money arrives as decimal POUND strings (e.g.
// "1234.50") — never do arithmetic on them, just format for display.

// One month's bar group on the Cashflow chart. `outgoing` is a positive
// magnitude (the backend already flipped the sign), so both draw as positive bars.
export const CashflowMonthSchema = z.object({
  month: z.string(), // ISO first-of-month, e.g. "2025-07-01"
  incoming: z.string(),
  outgoing: z.string(),
})
export type CashflowMonth = z.infer<typeof CashflowMonthSchema>

// The Cashflow card: exactly 12 monthly buckets (oldest→newest) + the window
// totals. balance = incoming − outgoing (NET cashflow over the window; may be
// negative), distinct from the actual bank balance.
export const CashflowSchema = z.object({
  months: z.array(CashflowMonthSchema),
  incoming: z.string(),
  outgoing: z.string(),
  balance: z.string(),
})
export type Cashflow = z.infer<typeof CashflowSchema>

// GET /api/v1/overview/cashflow envelope.
export const CashflowResponseSchema = z.object({ cashflow: CashflowSchema })

// One month's stacked bar on the Invoice Timeline: SENT invoices' totals split
// into the three status series (each invoice's whole total in exactly one).
export const InvoiceTimelineMonthSchema = z.object({
  month: z.string(), // ISO first-of-month
  overdue: z.string(),
  due: z.string(),
  paid: z.string(),
})
export type InvoiceTimelineMonth = z.infer<typeof InvoiceTimelineMonthSchema>

// The Invoice Timeline card: the monthly buckets (oldest→newest) + the headline
// Outstanding figure (total unpaid SENT receivables, not window-bound).
export const InvoiceTimelineSchema = z.object({
  months: z.array(InvoiceTimelineMonthSchema),
  outstanding: z.string(),
})
export type InvoiceTimeline = z.infer<typeof InvoiceTimelineSchema>

// GET /api/v1/overview/invoice-timeline envelope.
export const InvoiceTimelineResponseSchema = z.object({ invoice_timeline: InvoiceTimelineSchema })

// One month-end point on the Banking balance-over-time chart (the org's total
// balance across all live accounts at that month end).
export const BankBalancePointSchema = z.object({
  month: z.string(), // ISO first-of-month
  balance: z.string(), // pounds, signed
})
export type BankBalancePoint = z.infer<typeof BankBalancePointSchema>

// The Banking card: the month-end balance series + the current total balance +
// the live-account count.
export const BankingSchema = z.object({
  months: z.array(BankBalancePointSchema),
  balance: z.string(),
  accounts: z.number(),
})
export type Banking = z.infer<typeof BankingSchema>

// GET /api/v1/overview/banking envelope.
export const BankingResponseSchema = z.object({ banking: BankingSchema })
