import { apiFetch } from '@/lib/api'
import {
  GetOverviewResponseSchema,
  GetPayRunResponseSchema,
  ListPayRunsResponseSchema,
  GetPayslipResponseSchema,
  type Overview,
  type PayRun,
  type Payslip,
  type PreparePayRunRequest,
  type UpdatePayslipRequest,
} from '@/types/payroll'

// All payroll endpoints are owner/admin only; the bearer token is attached by
// apiFetch and a 401/403 surfaces as an ApiError for the view to show.

// GET /api/v1/payroll/overview — Status + Year-to-date + History + Employees.
export async function getPayrollOverview(taxYear?: number): Promise<Overview> {
  const qs = taxYear ? `?tax_year=${taxYear}` : ''
  const data = await apiFetch<unknown>(`/payroll/overview${qs}`, { method: 'GET' })
  return GetOverviewResponseSchema.parse(data).overview
}

// GET /api/v1/payroll/periods — the year's pay runs (history list, no payslips).
export async function listPayRuns(taxYear?: number): Promise<PayRun[]> {
  const qs = taxYear ? `?tax_year=${taxYear}` : ''
  const data = await apiFetch<unknown>(`/payroll/periods${qs}`, { method: 'GET' })
  return ListPayRunsResponseSchema.parse(data).pay_runs ?? []
}

// POST /api/v1/payroll/periods — prepare (or return the existing) draft run for a
// month, snapshotting active employees into payslips.
export async function preparePayRun(payload: PreparePayRunRequest): Promise<PayRun> {
  const data = await apiFetch<unknown>('/payroll/periods', { method: 'POST', body: payload })
  return GetPayRunResponseSchema.parse(data).pay_run
}

// GET /api/v1/payroll/periods/:id — one run with its payslips.
export async function getPayRun(id: string): Promise<PayRun> {
  const data = await apiFetch<unknown>(`/payroll/periods/${encodeURIComponent(id)}`, { method: 'GET' })
  return GetPayRunResponseSchema.parse(data).pay_run
}

// POST /api/v1/payroll/periods/:id/complete — "Run & Report" (finalise; RTI deferred).
export async function completePayRun(id: string): Promise<PayRun> {
  const data = await apiFetch<unknown>(`/payroll/periods/${encodeURIComponent(id)}/complete`, { method: 'POST' })
  return GetPayRunResponseSchema.parse(data).pay_run
}

// DELETE /api/v1/payroll/periods/:id — delete the latest run (204 No Content).
export async function deletePayRun(id: string): Promise<void> {
  await apiFetch<unknown>(`/payroll/periods/${encodeURIComponent(id)}`, { method: 'DELETE' })
}

// GET /api/v1/payroll/payslips/:id — one payslip.
export async function getPayslip(id: string): Promise<Payslip> {
  const data = await apiFetch<unknown>(`/payroll/payslips/${encodeURIComponent(id)}`, { method: 'GET' })
  return GetPayslipResponseSchema.parse(data).payslip
}

// PUT /api/v1/payroll/payslips/:id — edit a payslip (recomputes tax/NI; DRAFT run only).
export async function updatePayslip(id: string, payload: UpdatePayslipRequest): Promise<Payslip> {
  const data = await apiFetch<unknown>(`/payroll/payslips/${encodeURIComponent(id)}`, {
    method: 'PUT',
    body: payload,
  })
  return GetPayslipResponseSchema.parse(data).payslip
}
