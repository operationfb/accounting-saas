-- =============================================================================
-- PAYROLL MODULE — SQLC QUERIES
-- File: db/queries/payroll.sql
--
-- Type-safe Go for the pay_runs + payslips tables and the four GLOBAL rate tables
-- defined in db/schema/payroll_schema.sql, plus read-only snapshot joins over the
-- existing employee_payroll / organisation_memberships / users tables (loaded from
-- auth_schema.sql in this block's schema list). Generated into package `payroll`
-- at db/payroll.
--
-- The pay_runs / payslips queries are ORGANISATION-SCOPED (multi-tenancy) and the
-- header is SOFT-DELETE-AWARE (deleted_at IS NULL). The rate-table reads are GLOBAL
-- (statutory data shared by every org), keyed only by tax_year_start.
-- =============================================================================


-- =============================================================================
-- RATE TABLES (global reference data, read-only at runtime)
-- =============================================================================

-- name: GetTaxYear :one
SELECT * FROM payroll_tax_years
WHERE tax_year_start = $1;

-- name: ListPayeBands :many
SELECT region, band_order, upper_threshold_minor, rate_bps
FROM payroll_paye_bands
WHERE tax_year_start = $1
ORDER BY region, band_order;

-- name: GetNiThresholds :one
SELECT * FROM payroll_ni_thresholds
WHERE tax_year_start = $1;

-- name: ListNiCategoryRates :many
SELECT category_letter, employee_main_bps, employee_upper_bps, employer_bps
FROM payroll_ni_category_rates
WHERE tax_year_start = $1
ORDER BY category_letter;


-- =============================================================================
-- PAY RUNS (the "Month N" header)
-- =============================================================================

-- name: CreatePayRun :one
INSERT INTO pay_runs (
    organisation_id,
    created_by_user_id,
    tax_year_start,
    period,
    period_start,
    period_end,
    payment_date,
    employment_allowance_claimed
) VALUES (
    $1, $2, $3, $4, $5, $6, $7, $8
)
RETURNING *;

-- name: GetPayRun :one
SELECT * FROM pay_runs
WHERE id = $1
  AND organisation_id = $2
  AND deleted_at IS NULL;

-- name: ListPayRuns :many
-- The org's runs for a tax year, newest period first (the overview History list).
SELECT * FROM pay_runs
WHERE organisation_id = $1
  AND tax_year_start = $2
  AND deleted_at IS NULL
ORDER BY period DESC;

-- name: GetLatestPayRun :one
-- The most recent live run for a year — used for the sequencing check (next period
-- = latest + 1) AND the edit-lock (only the latest run may be edited/deleted).
SELECT * FROM pay_runs
WHERE organisation_id = $1
  AND tax_year_start = $2
  AND deleted_at IS NULL
ORDER BY period DESC
LIMIT 1;

-- name: UpdatePayRunEmploymentAllowance :exec
-- Set the EA offset on a run without changing its status (recomputed whenever the
-- run's payslips change, so the draft's "Due to HMRC" reflects the allowance).
UPDATE pay_runs SET
    employment_allowance_amount_minor = $3,
    updated_at                        = now()
WHERE id = $1
  AND organisation_id = $2;

-- name: CompletePayRun :one
-- "Run & Report": finalise the run and record the Employment Allowance offset.
UPDATE pay_runs SET
    status                            = 'completed',
    employment_allowance_amount_minor = $3,
    updated_at                        = now()
WHERE id = $1
  AND organisation_id = $2
RETURNING *;

-- name: SoftDeletePayRun :exec
UPDATE pay_runs SET
    deleted_at = now(),
    updated_at = now()
WHERE id = $1
  AND organisation_id = $2;


-- =============================================================================
-- PAYSLIPS
-- =============================================================================

-- name: CreatePayslip :one
INSERT INTO payslips (
    organisation_id,
    pay_run_id,
    user_id,
    tax_code,
    ni_category_letter,
    nic_calculation,
    week1_month1_basis,
    student_loan_undergraduate,
    student_loan_postgraduate,
    basic_pay_minor,
    allowance_minor,
    other_payments_minor,
    pay_not_subject_to_tax_ni_minor,
    payroll_giving_minor,
    other_deductions_net_pay_minor,
    items_class1_nic_not_paye_minor,
    salary_sacrifice_deductions_minor,
    leaving_payslip
) VALUES (
    $1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18
)
RETURNING *;

-- name: CreatePayslipComputed :batchexec
-- Bulk-insert path used by PreparePayRun: one INSERT per employee, pipelined in a
-- single round-trip (pgx batch). Unlike CreatePayslip this stores the engine's
-- COMPUTED figures too (the service computes them in-memory before inserting), so
-- there's no follow-up UPDATE per payslip. Columns not listed default to 0/now().
INSERT INTO payslips (
    organisation_id,
    pay_run_id,
    user_id,
    tax_code,
    ni_category_letter,
    nic_calculation,
    week1_month1_basis,
    student_loan_undergraduate,
    student_loan_postgraduate,
    basic_pay_minor,
    allowance_minor,
    other_payments_minor,
    pay_not_subject_to_tax_ni_minor,
    payroll_giving_minor,
    other_deductions_net_pay_minor,
    items_class1_nic_not_paye_minor,
    salary_sacrifice_deductions_minor,
    leaving_payslip,
    gross_pay_minor,
    taxable_pay_minor,
    niable_pay_minor,
    tax_deducted_minor,
    employee_ni_minor,
    employer_ni_minor,
    net_pay_minor
) VALUES (
    $1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18,
    $19, $20, $21, $22, $23, $24, $25
);

-- name: GetPayslip :one
SELECT * FROM payslips
WHERE id = $1
  AND organisation_id = $2;

-- name: DeletePayslipsForRun :exec
-- Remove all payslips of a draft run (used by "Refresh from profiles", which then
-- re-snapshots them from the current employee profiles).
DELETE FROM payslips
WHERE pay_run_id = $1
  AND organisation_id = $2;

-- name: ListPayslipsForRun :many
SELECT * FROM payslips
WHERE pay_run_id = $1
  AND organisation_id = $2
ORDER BY created_at;

-- name: UpdatePayslipInputs :one
-- Apply the editable inputs from the Edit Payslip screen (the snapshot config and
-- the computed columns are written separately).
UPDATE payslips SET
    basic_pay_minor                          = $3,
    overtime_minor                           = $4,
    bonus_minor                              = $5,
    commission_minor                         = $6,
    allowance_minor                          = $7,
    absence_payments_minor                   = $8,
    holiday_pay_minor                        = $9,
    other_payments_minor                     = $10,
    pay_not_subject_to_tax_ni_minor          = $11,
    statutory_sick_pay_minor                 = $12,
    statutory_maternity_pay_minor            = $13,
    statutory_paternity_pay_minor            = $14,
    statutory_adoption_pay_minor             = $15,
    shared_parental_pay_minor                = $16,
    statutory_neonatal_care_pay_minor        = $17,
    statutory_parental_bereavement_pay_minor = $18,
    payroll_giving_minor                     = $19,
    other_deductions_net_pay_minor           = $20,
    items_class1_nic_not_paye_minor          = $21,
    salary_sacrifice_deductions_minor        = $22,
    tax_code                                 = $23,
    ni_category_letter                       = $24,
    nic_calculation                          = $25,
    week1_month1_basis                       = $26,
    student_loan_undergraduate               = $27,
    student_loan_postgraduate                = $28,
    hours_worked                             = $29,
    comment                                  = $30,
    updated_at                               = now()
WHERE id = $1
  AND organisation_id = $2
RETURNING *;

-- name: UpdatePayslipComputed :one
-- Write the engine's computed figures back onto the payslip.
UPDATE payslips SET
    gross_pay_minor        = $3,
    taxable_pay_minor      = $4,
    niable_pay_minor       = $5,
    tax_deducted_minor     = $6,
    employee_ni_minor      = $7,
    employer_ni_minor      = $8,
    employee_pension_minor = $9,
    employer_pension_minor = $10,
    student_loan_minor     = $11,
    net_pay_minor          = $12,
    updated_at             = now()
WHERE id = $1
  AND organisation_id = $2
RETURNING *;


-- =============================================================================
-- AGGREGATES (year-to-date — derived, never stored as a running total)
-- =============================================================================

-- name: SumYearToDateForUser :one
-- Cumulative figures for one employee across a tax year, up to AND INCLUDING the
-- given period. Pass (period - 1) for the "prior" YTD the cumulative engine needs;
-- pass the current period for the payslip's YTD display. Joins payslips → pay_runs
-- for the tax year + period filter.
SELECT
    COALESCE(SUM(p.gross_pay_minor), 0)::BIGINT    AS gross_pay_minor,
    COALESCE(SUM(p.taxable_pay_minor), 0)::BIGINT  AS taxable_pay_minor,
    COALESCE(SUM(p.niable_pay_minor), 0)::BIGINT   AS niable_pay_minor,
    COALESCE(SUM(p.tax_deducted_minor), 0)::BIGINT AS tax_deducted_minor,
    COALESCE(SUM(p.employee_ni_minor), 0)::BIGINT  AS employee_ni_minor,
    COALESCE(SUM(p.employer_ni_minor), 0)::BIGINT  AS employer_ni_minor,
    COALESCE(SUM(p.net_pay_minor), 0)::BIGINT      AS net_pay_minor
FROM payslips p
JOIN pay_runs r ON r.id = p.pay_run_id AND r.deleted_at IS NULL
WHERE p.organisation_id = $1
  AND p.user_id = $2
  AND r.tax_year_start = $3
  AND r.period <= $4;

-- name: ListYearToDateByUserUpToPeriod :many
-- Per-employee year-to-date totals for the org in ONE query, summed over periods UP
-- TO AND INCLUDING $3. Avoids an N+1 over the employee list. Used three ways:
--   - prepare: pass (period - 1) for the cumulative "prior" YTD the engine needs;
--   - run detail / payslip YTD block: pass the run's period;
--   - overview: pass 12 (the whole year).
SELECT
    p.user_id,
    COALESCE(SUM(p.gross_pay_minor), 0)::BIGINT    AS gross_pay_minor,
    COALESCE(SUM(p.taxable_pay_minor), 0)::BIGINT  AS taxable_pay_minor,
    COALESCE(SUM(p.niable_pay_minor), 0)::BIGINT   AS niable_pay_minor,
    COALESCE(SUM(p.tax_deducted_minor), 0)::BIGINT AS tax_deducted_minor,
    COALESCE(SUM(p.employee_ni_minor), 0)::BIGINT  AS employee_ni_minor,
    COALESCE(SUM(p.employer_ni_minor), 0)::BIGINT  AS employer_ni_minor,
    COALESCE(SUM(p.net_pay_minor), 0)::BIGINT      AS net_pay_minor
FROM payslips p
JOIN pay_runs r ON r.id = p.pay_run_id AND r.deleted_at IS NULL
WHERE p.organisation_id = $1
  AND r.tax_year_start = $2
  AND r.period <= $3
GROUP BY p.user_id;

-- name: ListRunPayslipNames :many
-- The employee display names for a run's payslips, in ONE query (so the detail
-- builder doesn't fetch each user individually).
SELECT p.user_id, u.first_name, u.last_name
FROM payslips p
JOIN users u ON u.id = p.user_id
WHERE p.pay_run_id = $1
  AND p.organisation_id = $2;

-- name: SumOrgYearToDate :one
-- Whole-organisation year-to-date totals for the overview's Year-to-date card.
SELECT
    COALESCE(SUM(p.gross_pay_minor), 0)::BIGINT    AS gross_pay_minor,
    COALESCE(SUM(p.tax_deducted_minor), 0)::BIGINT AS tax_deducted_minor,
    COALESCE(SUM(p.employee_ni_minor + p.employer_ni_minor), 0)::BIGINT AS total_ni_minor,
    COALESCE(SUM(r.employment_allowance_amount_minor), 0)::BIGINT AS employment_allowance_minor
FROM payslips p
JOIN pay_runs r ON r.id = p.pay_run_id AND r.deleted_at IS NULL
WHERE p.organisation_id = $1
  AND r.tax_year_start = $2;

-- name: SumPayRunTotals :one
-- Per-run totals for the History row (pay/tax/NI/net for one run).
SELECT
    COALESCE(SUM(gross_pay_minor), 0)::BIGINT    AS gross_pay_minor,
    COALESCE(SUM(tax_deducted_minor), 0)::BIGINT AS tax_deducted_minor,
    COALESCE(SUM(employee_ni_minor), 0)::BIGINT  AS employee_ni_minor,
    COALESCE(SUM(employer_ni_minor), 0)::BIGINT  AS employer_ni_minor,
    COALESCE(SUM(net_pay_minor), 0)::BIGINT      AS net_pay_minor
FROM payslips
WHERE pay_run_id = $1
  AND organisation_id = $2;


-- =============================================================================
-- SNAPSHOT SOURCE (active employees + their payroll profile)
-- =============================================================================

-- name: ListActivePayrollEmployees :many
-- Every ACTIVE member of the org with their employee_payroll profile (LEFT JOIN, so
-- a member with no saved profile still appears, read as column defaults via COALESCE).
-- This is what PreparePayRun snapshots into draft payslips.
SELECT
    u.id                                                        AS user_id,
    u.first_name,
    u.last_name,
    ep.tax_code                                                 AS tax_code,
    COALESCE(ep.ni_category_letter, 'A')                        AS ni_category_letter,
    COALESCE(ep.nic_calculation, 'employee')                    AS nic_calculation,
    COALESCE(ep.week1_month1_basis, FALSE)                      AS week1_month1_basis,
    COALESCE(ep.student_loan_undergraduate, FALSE)              AS student_loan_undergraduate,
    COALESCE(ep.student_loan_postgraduate, FALSE)               AS student_loan_postgraduate,
    COALESCE(ep.basic_pay_minor, 0)::BIGINT                     AS basic_pay_minor,
    COALESCE(ep.allowance_minor, 0)::BIGINT                     AS allowance_minor,
    COALESCE(ep.other_payments_minor, 0)::BIGINT               AS other_payments_minor,
    COALESCE(ep.pay_not_subject_to_tax_ni_minor, 0)::BIGINT     AS pay_not_subject_to_tax_ni_minor,
    COALESCE(ep.payroll_giving_minor, 0)::BIGINT                AS payroll_giving_minor,
    COALESCE(ep.other_deductions_net_pay_minor, 0)::BIGINT      AS other_deductions_net_pay_minor,
    COALESCE(ep.items_class1_nic_not_paye_minor, 0)::BIGINT     AS items_class1_nic_not_paye_minor,
    COALESCE(ep.salary_sacrifice_deductions_minor, 0)::BIGINT   AS salary_sacrifice_deductions_minor,
    COALESCE(ep.leaving_next_pay_run, FALSE)                    AS leaving_next_pay_run,
    COALESCE(ep.pension_status, 'opted_out_or_ineligible')      AS pension_status,
    ep.start_date                                               AS start_date,
    ep.leaving_date                                             AS leaving_date
FROM organisation_memberships m
JOIN users u ON u.id = m.user_id AND u.deleted_at IS NULL
LEFT JOIN employee_payroll ep
    ON ep.organisation_id = m.organisation_id AND ep.user_id = m.user_id
WHERE m.organisation_id = $1
  AND m.status = 'active'
ORDER BY u.last_name, u.first_name;


-- name: GetOrganisationPayrollSettings :one
-- The org's payroll references for the overview/payslip header (PAYE ref, etc.).
SELECT
    name,
    paye_reference,
    accounts_office_reference,
    claims_employment_allowance,
    address_line_1,
    address_line_2,
    address_line_3,
    town,
    region,
    postcode
FROM organisations
WHERE id = $1
  AND deleted_at IS NULL;
