import { apiFetch } from '@/lib/api'
import {
  CashflowResponseSchema,
  InvoiceTimelineResponseSchema,
  BankingResponseSchema,
  type Cashflow,
  type InvoiceTimeline,
  type Banking,
} from '@/types/overview'

// GET /api/v1/overview/cashflow — money in vs money out per month over the last
// 12 months, plus the window totals + net Balance. The bearer token is attached by
// apiFetch, and a 401 (expired/invalid token) is handled there (logout + redirect
// to /login). Any active member of the org may read it.
export async function getCashflow(): Promise<Cashflow> {
  const data = await apiFetch<unknown>('/overview/cashflow', { method: 'GET' })
  return CashflowResponseSchema.parse(data).cashflow
}

// GET /api/v1/overview/invoice-timeline — SENT invoices' totals per month split
// into Overdue / Due / Paid, plus the Outstanding figure. Bearer/401 handled by
// apiFetch; any active member may read it.
export async function getInvoiceTimeline(): Promise<InvoiceTimeline> {
  const data = await apiFetch<unknown>('/overview/invoice-timeline', { method: 'GET' })
  return InvoiceTimelineResponseSchema.parse(data).invoice_timeline
}

// GET /api/v1/overview/banking — the org's month-end total bank balance over the
// last 12 months, plus the current total balance + live-account count. Bearer/401
// handled by apiFetch; any active member may read it.
export async function getBanking(): Promise<Banking> {
  const data = await apiFetch<unknown>('/overview/banking', { method: 'GET' })
  return BankingResponseSchema.parse(data).banking
}
