import { z } from 'zod'

// Payroll API types — mirror the Go DTOs in internal/payroll/dto.go. Money arrives
// as decimal POUND strings ("1047.00") — format for display, never do arithmetic on
// them. Dates are "YYYY-MM-DD". The pay-run / payslip engine is owner/admin only.

// The year-to-date block on a payslip (cumulative through the payslip's period).
export const PayslipYTDSchema = z.object({
  gross_pay: z.string(),
  tax_deducted: z.string(),
  employee_ni: z.string(),
  employer_ni: z.string(),
  net_pay: z.string(),
})

// One employee's payslip: snapshot config + editable inputs + computed outputs.
export const PayslipSchema = z.object({
  id: z.string(),
  pay_run_id: z.string(),
  user_id: z.string(),
  employee_name: z.string(),

  tax_code: z.string().nullish(),
  ni_category_letter: z.string(),
  nic_calculation: z.string(),
  week1_month1_basis: z.boolean(),
  student_loan_undergraduate: z.boolean(),
  student_loan_postgraduate: z.boolean(),

  // Pay inputs (pounds)
  basic_pay: z.string(),
  overtime: z.string(),
  bonus: z.string(),
  commission: z.string(),
  allowance: z.string(),
  absence_payments: z.string(),
  holiday_pay: z.string(),
  other_payments: z.string(),
  pay_not_subject_to_tax_ni: z.string(),

  // Statutory pay (pounds)
  statutory_sick_pay: z.string(),
  statutory_maternity_pay: z.string(),
  statutory_paternity_pay: z.string(),
  statutory_adoption_pay: z.string(),
  shared_parental_pay: z.string(),
  statutory_neonatal_care_pay: z.string(),
  statutory_parental_bereavement_pay: z.string(),

  // Deductions (pounds)
  payroll_giving: z.string(),
  other_deductions_net_pay: z.string(),
  items_class1_nic_not_paye: z.string(),
  salary_sacrifice_deductions: z.string(),

  // Computed outputs (pounds)
  gross_pay: z.string(),
  taxable_pay: z.string(),
  niable_pay: z.string(),
  tax_deducted: z.string(),
  employee_ni: z.string(),
  employer_ni: z.string(),
  employee_pension: z.string(),
  employer_pension: z.string(),
  student_loan: z.string(),
  net_pay: z.string(),

  year_to_date: PayslipYTDSchema,

  hours_worked: z.string().nullish(),
  comment: z.string().nullish(),
  leaving_payslip: z.boolean(),
})
export type Payslip = z.infer<typeof PayslipSchema>

export const PayRunTotalsSchema = z.object({
  pay: z.string(),
  tax: z.string(),
  employee_ni: z.string(),
  employer_ni: z.string(),
  net_pay: z.string(),
  due_to_hmrc: z.string(),
})

export const PayRunSchema = z.object({
  id: z.string(),
  organisation_id: z.string(),
  tax_year_start: z.number(),
  period: z.number(),
  period_start: z.string(),
  period_end: z.string(),
  payment_date: z.string(),
  status: z.string(), // draft | completed
  employment_allowance_claimed: z.boolean(),
  employment_allowance_amount: z.string(),
  totals: PayRunTotalsSchema,
  payslips: z.array(PayslipSchema).nullish(),
  created_at: z.string(),
  updated_at: z.string(),
})
export type PayRun = z.infer<typeof PayRunSchema>

export const OverviewStatusSchema = z.object({
  current_period: z.number(),
  current_run_id: z.string().nullish(),
  state: z.string(), // none | draft | completed_unfiled
  filing_deadline: z.string().nullish(),
  next_period: z.number().nullish(),
  can_prepare: z.boolean(),
})

export const OverviewYTDSchema = z.object({
  total_pay: z.string(),
  total_tax: z.string(),
  total_ni: z.string(),
  employment_allowance: z.string(),
})

export const OverviewEmployeeSchema = z.object({
  user_id: z.string(),
  name: z.string(),
  start_date: z.string().nullish(),
  monthly_pay: z.string(),
  total_pay: z.string(),
  total_tax: z.string(),
  auto_enrolment: z.string(),
})

export const OverviewSchema = z.object({
  tax_year: z.number(),
  tax_year_label: z.string(),
  company_name: z.string(),
  status: OverviewStatusSchema,
  year_to_date: OverviewYTDSchema,
  history: z.array(PayRunSchema).nullish(),
  employees: z.array(OverviewEmployeeSchema).nullish(),
})
export type Overview = z.infer<typeof OverviewSchema>

// Response envelopes
export const GetOverviewResponseSchema = z.object({ overview: OverviewSchema })
export const GetPayRunResponseSchema = z.object({ pay_run: PayRunSchema })
export const ListPayRunsResponseSchema = z.object({ pay_runs: z.array(PayRunSchema).nullish() })
export const GetPayslipResponseSchema = z.object({ payslip: PayslipSchema })

// Request bodies
export interface PreparePayRunRequest {
  tax_year?: number
  period: number
  payment_date: string // YYYY-MM-DD
}

// The editable Edit Payslip body (Go UpdatePayslipRequest). All money fields are
// pound strings (blank = 0).
export interface UpdatePayslipRequest {
  tax_code?: string | null
  ni_category_letter: string
  nic_calculation: string
  week1_month1_basis: boolean
  student_loan_undergraduate: boolean
  student_loan_postgraduate: boolean
  basic_pay: string
  overtime: string
  bonus: string
  commission: string
  allowance: string
  absence_payments: string
  holiday_pay: string
  other_payments: string
  pay_not_subject_to_tax_ni: string
  statutory_sick_pay: string
  statutory_maternity_pay: string
  statutory_paternity_pay: string
  statutory_adoption_pay: string
  shared_parental_pay: string
  statutory_neonatal_care_pay: string
  statutory_parental_bereavement_pay: string
  payroll_giving: string
  other_deductions_net_pay: string
  items_class1_nic_not_paye: string
  salary_sacrifice_deductions: string
  hours_worked?: string | null
  comment?: string | null
}
