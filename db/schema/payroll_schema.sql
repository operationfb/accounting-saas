-- =============================================================================
-- PAYROLL MODULE — DATABASE SCHEMA
-- Accounting SaaS Platform (UK-focused, HMRC MTD-ready)
-- PostgreSQL 15+
--
-- The pay-run / payslip engine, modelled on the FreeAgent Payroll resource
-- (https://dev.freeagent.com/docs/payroll). The employee PROFILE half already
-- exists (employee_payroll + the payroll identity columns on users, in
-- auth_schema.sql); THIS file adds the periodic side:
--
--   pay_runs   — one monthly tax-month run per organisation (the "Month N" header)
--   payslips   — one row per employee per run (a snapshot + the computed figures)
--
-- plus four GLOBAL reference tables holding the statutory HMRC rates per tax year
-- (income-tax bands, NI thresholds, per-category NI rates, the per-year config).
-- These are shared by every organisation (no organisation_id) and seeded from the
-- gov.uk "Rates and thresholds for employers" page.
--
-- Design decisions worth knowing:
--
--   MONEY IS BIGINT MINOR UNITS (PENCE), NEVER float/numeric for arithmetic.
--   Every *_minor column is whole pence (£42.50 = 4250). BIGINT, not INTEGER, so
--   cumulative year-to-date totals can exceed the int32 ceiling (~£21.4m).
--
--   PAYSLIP CONFIG IS A SNAPSHOT.
--   tax_code / ni_category_letter / nic_calculation / week1_month1_basis / the
--   student-loan flags are COPIED onto the payslip at prepare time from the
--   employee_payroll profile, so a later profile edit never rewrites filed history.
--
--   COMPUTED FIGURES ARE STORED, NOT RE-DERIVED ON READ.
--   The engine (internal/payroll/calc.go) writes gross/taxable/niable/tax/NI/net
--   onto the payslip on every save. The overview's Year-to-date is the SUM of the
--   stored period figures — derived data, never a stored running total (no drift).
--
--   ONLY THE LATEST RUN IS EDITABLE (enforced in the service, not the DB).
--   Cumulative PAYE/NI means an earlier month feeds the next, so we lock all but
--   the most recent run; to fix an earlier month you delete the latest run(s) and
--   re-run (mirrors FreeAgent's "Delete Month N Payroll").
--
--   RTI / HMRC FILING IS DEFERRED.
--   status only goes draft → completed ("Run & Report" pressed). There is no RTI
--   submission yet — a completed run reads as "report unfiled". See BACKLOG.md.
--
-- Application order (see sqlc.yaml): schema.sql (set_updated_at) → auth_schema.sql
-- (organisations, users, employee_payroll) → payroll_schema.sql (this file). The
-- FKs below are therefore declared INLINE.
-- =============================================================================


-- -----------------------------------------------------------------------------
-- payroll_tax_years  (per-year config — GLOBAL reference data)
-- One row per UK tax year, keyed by the calendar year it STARTS in (2026 = the
-- 2026/27 year that runs 6 Apr 2026 → 5 Apr 2027). Holds the Employment Allowance
-- cap and the default tax code. Parent of the three rate tables below.
-- -----------------------------------------------------------------------------
CREATE TABLE payroll_tax_years (
    tax_year_start                 INTEGER PRIMARY KEY,            -- e.g. 2026 → 2026/27
    label                          TEXT    NOT NULL,               -- e.g. '2026/27'
    employment_allowance_cap_minor BIGINT  NOT NULL DEFAULT 0,     -- annual EA cap (£10,500 = 1050000)
    default_tax_code               VARCHAR(10) NOT NULL DEFAULT '1257L',
    period_count                   INTEGER NOT NULL DEFAULT 12,    -- monthly → 12
    created_at                     TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at                     TIMESTAMPTZ NOT NULL DEFAULT now()
);


-- -----------------------------------------------------------------------------
-- payroll_paye_bands  (income-tax bands — GLOBAL, keyed by tax year + REGION)
-- The income-tax bands that sit ABOVE the personal allowance (the allowance comes
-- from the employee's tax-code number, not from here). Keyed by region so Scottish
-- and Welsh codes get their own band set:
--   rUK = England & Northern Ireland (no tax-code prefix)
--   S   = Scotland   (S-prefixed codes, e.g. S1257L) — more bands, different rates
--   C   = Wales      (C-prefixed codes) — currently identical to rUK, stored
--         separately so a future divergence is a data change, not a code change
-- The region is derived per-employee from the tax-code PREFIX; HMRC assigns the
-- prefix from the employee's residence — it is NOT a company-level setting.
--
-- upper_threshold_minor is the band's CUMULATIVE upper bound of taxable income
-- (after the allowance), in pence; NULL marks the open-ended TOP band. band_order
-- ascends from the lowest rate. NI has no regional variation (single set below).
-- -----------------------------------------------------------------------------
CREATE TABLE payroll_paye_bands (
    tax_year_start        INTEGER NOT NULL REFERENCES payroll_tax_years(tax_year_start) ON DELETE CASCADE,
    region                VARCHAR(4) NOT NULL CHECK (region IN ('rUK','S','C')),
    band_order            INTEGER NOT NULL,             -- 1 = lowest band, ascending
    upper_threshold_minor BIGINT,                       -- cumulative upper bound (pence); NULL = top band
    rate_bps              INTEGER NOT NULL,             -- 2000 = 20%, 4500 = 45%
    PRIMARY KEY (tax_year_start, region, band_order)
);


-- -----------------------------------------------------------------------------
-- payroll_ni_thresholds  (National Insurance thresholds — GLOBAL, per tax year)
-- Stores BOTH the annual and the published rounded MONTHLY figures, because HMRC
-- publishes the monthly numbers directly and £5,000/12 (= £416.67) does NOT equal
-- the published £417 monthly secondary threshold that yields the £42.45 employer
-- NI in our reference data. Employees are assessed on the MONTHLY figures; company
-- directors on the ANNUAL figures (cumulative across the year).
--   LEL = Lower Earnings Limit, PT = Primary Threshold (employee),
--   ST  = Secondary Threshold (employer), UEL = Upper Earnings Limit.
-- -----------------------------------------------------------------------------
CREATE TABLE payroll_ni_thresholds (
    tax_year_start    INTEGER PRIMARY KEY REFERENCES payroll_tax_years(tax_year_start) ON DELETE CASCADE,
    lel_annual_minor  BIGINT NOT NULL,
    lel_monthly_minor BIGINT NOT NULL,
    pt_annual_minor   BIGINT NOT NULL,
    pt_monthly_minor  BIGINT NOT NULL,
    st_annual_minor   BIGINT NOT NULL,
    st_monthly_minor  BIGINT NOT NULL,
    uel_annual_minor  BIGINT NOT NULL,
    uel_monthly_minor BIGINT NOT NULL
);


-- -----------------------------------------------------------------------------
-- payroll_ni_category_rates  (per NI category letter — GLOBAL, per tax year)
-- The contribution rates for each NI category letter (A is the standard letter).
--   employee_main_bps  — employee rate between PT and UEL
--   employee_upper_bps — employee rate above UEL
--   employer_bps       — employer (secondary) rate above ST
-- -----------------------------------------------------------------------------
CREATE TABLE payroll_ni_category_rates (
    tax_year_start     INTEGER NOT NULL REFERENCES payroll_tax_years(tax_year_start) ON DELETE CASCADE,
    category_letter    VARCHAR(2) NOT NULL,
    employee_main_bps  INTEGER NOT NULL,             -- 800 = 8%
    employee_upper_bps INTEGER NOT NULL,             -- 200 = 2%
    employer_bps       INTEGER NOT NULL,             -- 1500 = 15%
    PRIMARY KEY (tax_year_start, category_letter)
);


-- -----------------------------------------------------------------------------
-- pay_runs  (the "Month N" header — org-scoped, soft-deletable)
-- One run per (organisation, tax_year_start, period) while live. period is the tax
-- MONTH (1 = 6 Apr→5 May … 12 = 6 Mar→5 Apr). payment_date is when staff are paid
-- and must fall within the period window (validated in the service).
-- -----------------------------------------------------------------------------
CREATE TABLE pay_runs (
    id                  UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    organisation_id     UUID NOT NULL REFERENCES organisations(id),   -- tenant
    created_by_user_id  UUID NOT NULL REFERENCES users(id),           -- who ran it (audit)

    tax_year_start      INTEGER NOT NULL,                 -- e.g. 2026 → 2026/27
    frequency           VARCHAR(10) NOT NULL DEFAULT 'monthly'
                        CHECK (frequency IN ('monthly')),  -- weekly etc. deferred
    period              INTEGER NOT NULL CHECK (period BETWEEN 1 AND 12),  -- tax month

    period_start        DATE NOT NULL,                    -- 6th of the month (derived)
    period_end          DATE NOT NULL,                    -- 5th of next month (derived)
    payment_date        DATE NOT NULL,                    -- pay day (within the window)

    -- draft     = being prepared/edited (the only editable state)
    -- completed = "Run & Report" pressed; finalised. RTI still unfiled (deferred).
    status              VARCHAR(20) NOT NULL DEFAULT 'draft'
                        CHECK (status IN ('draft','completed')),

    -- Employment Allowance: the org-level annual NI relief, applied at run level to
    -- offset employer NI. claimed defaults TRUE; amount is this run's offset (pence).
    employment_allowance_claimed       BOOLEAN NOT NULL DEFAULT TRUE,
    employment_allowance_amount_minor  BIGINT  NOT NULL DEFAULT 0,

    deleted_at          TIMESTAMPTZ,                      -- soft delete
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- One live run per period per org.
CREATE UNIQUE INDEX uq_pay_runs_period ON pay_runs (organisation_id, tax_year_start, period)
    WHERE deleted_at IS NULL;

-- Backs the overview/history list (org + year, newest first) and GetLatestPayRun.
CREATE INDEX idx_pay_runs_org_year ON pay_runs (organisation_id, tax_year_start) WHERE deleted_at IS NULL;


-- -----------------------------------------------------------------------------
-- payslips  (one per employee per run)
-- Child of pay_runs (ON DELETE CASCADE, so deleting a draft run removes its
-- payslips). organisation_id is denormalised for org-scoped queries. The config
-- columns are a SNAPSHOT of the employee_payroll profile at prepare time; the
-- *_minor INPUT columns are editable on the Edit Payslip screen; the COMPUTED
-- columns are written by the engine on every save.
-- -----------------------------------------------------------------------------
CREATE TABLE payslips (
    id                  UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    organisation_id     UUID NOT NULL REFERENCES organisations(id),   -- denormalised tenant
    pay_run_id          UUID NOT NULL REFERENCES pay_runs(id) ON DELETE CASCADE,
    user_id             UUID NOT NULL REFERENCES users(id),           -- the employee (claimant)

    -- --- Snapshot of the profile config (so history can't be rewritten) ---------
    tax_code                  VARCHAR(10),
    ni_category_letter        VARCHAR(2)  NOT NULL DEFAULT 'A',
    nic_calculation           VARCHAR(20) NOT NULL DEFAULT 'employee',  -- director|director_alternative|employee
    week1_month1_basis        BOOLEAN     NOT NULL DEFAULT FALSE,
    student_loan_undergraduate BOOLEAN    NOT NULL DEFAULT FALSE,
    student_loan_postgraduate  BOOLEAN    NOT NULL DEFAULT FALSE,

    -- --- Period INPUTS (editable; pence) ---------------------------------------
    basic_pay_minor                  BIGINT NOT NULL DEFAULT 0,
    overtime_minor                   BIGINT NOT NULL DEFAULT 0,
    bonus_minor                      BIGINT NOT NULL DEFAULT 0,
    commission_minor                 BIGINT NOT NULL DEFAULT 0,
    allowance_minor                  BIGINT NOT NULL DEFAULT 0,
    absence_payments_minor           BIGINT NOT NULL DEFAULT 0,
    holiday_pay_minor                BIGINT NOT NULL DEFAULT 0,
    other_payments_minor             BIGINT NOT NULL DEFAULT 0,
    pay_not_subject_to_tax_ni_minor  BIGINT NOT NULL DEFAULT 0,

    -- --- Statutory pay (editable; pence — NOT auto-calculated in v1) ------------
    statutory_sick_pay_minor                 BIGINT NOT NULL DEFAULT 0,
    statutory_maternity_pay_minor            BIGINT NOT NULL DEFAULT 0,
    statutory_paternity_pay_minor            BIGINT NOT NULL DEFAULT 0,
    statutory_adoption_pay_minor             BIGINT NOT NULL DEFAULT 0,
    shared_parental_pay_minor                BIGINT NOT NULL DEFAULT 0,
    statutory_neonatal_care_pay_minor        BIGINT NOT NULL DEFAULT 0,
    statutory_parental_bereavement_pay_minor BIGINT NOT NULL DEFAULT 0,

    -- --- Deductions (editable; pence) ------------------------------------------
    payroll_giving_minor             BIGINT NOT NULL DEFAULT 0,
    other_deductions_net_pay_minor   BIGINT NOT NULL DEFAULT 0,
    items_class1_nic_not_paye_minor  BIGINT NOT NULL DEFAULT 0,
    salary_sacrifice_deductions_minor BIGINT NOT NULL DEFAULT 0,

    -- --- COMPUTED outputs (written by the engine; pence) -----------------------
    gross_pay_minor      BIGINT NOT NULL DEFAULT 0,   -- all pay incl. statutory
    taxable_pay_minor    BIGINT NOT NULL DEFAULT 0,   -- subject to PAYE this period
    niable_pay_minor     BIGINT NOT NULL DEFAULT 0,   -- subject to NI this period
    tax_deducted_minor   BIGINT NOT NULL DEFAULT 0,   -- PAYE (can be negative on a cumulative refund)
    employee_ni_minor    BIGINT NOT NULL DEFAULT 0,
    employer_ni_minor    BIGINT NOT NULL DEFAULT 0,
    employee_pension_minor BIGINT NOT NULL DEFAULT 0, -- 0 in v1 (contributions deferred)
    employer_pension_minor BIGINT NOT NULL DEFAULT 0, -- 0 in v1
    student_loan_minor   BIGINT NOT NULL DEFAULT 0,   -- 0 in v1 (deductions deferred)
    net_pay_minor        BIGINT NOT NULL DEFAULT 0,

    -- --- Misc ------------------------------------------------------------------
    hours_worked        NUMERIC(8,2),
    comment             TEXT,
    leaving_payslip     BOOLEAN NOT NULL DEFAULT FALSE,

    created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT now(),

    -- One payslip per employee per run.
    UNIQUE (pay_run_id, user_id)
);

-- Fetch a run's payslips.
CREATE INDEX idx_payslips_run ON payslips (pay_run_id);
-- Year-to-date aggregation: an employee's payslips across a tax year (joined to
-- pay_runs for tax_year_start; the index keeps the per-user lookup cheap).
CREATE INDEX idx_payslips_org_user ON payslips (organisation_id, user_id);


-- =============================================================================
-- TRIGGERS — auto-update updated_at (reuses set_updated_at() from schema.sql)
-- =============================================================================
CREATE TRIGGER trg_payroll_tax_years_updated_at
    BEFORE UPDATE ON payroll_tax_years
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

CREATE TRIGGER trg_pay_runs_updated_at
    BEFORE UPDATE ON pay_runs
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

CREATE TRIGGER trg_payslips_updated_at
    BEFORE UPDATE ON payslips
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();


-- =============================================================================
-- COMMENTS
-- =============================================================================
COMMENT ON TABLE  pay_runs IS 'Monthly payroll run (the "Month N" header). Org-scoped, soft-deleted. Only the latest run per year is editable (service-enforced). status: draft|completed; RTI filing deferred.';
COMMENT ON TABLE  payslips IS 'One employee payslip per pay run. Config columns are a snapshot of the employee_payroll profile; *_minor inputs are editable; the computed gross/tax/NI/net are written by internal/payroll/calc.go.';
COMMENT ON TABLE  payroll_paye_bands IS 'Income-tax bands above the personal allowance, GLOBAL reference data keyed by tax year + region (rUK/S/C). Region is derived per-employee from the tax-code prefix, not the company.';
COMMENT ON TABLE  payroll_ni_thresholds IS 'NI thresholds per tax year, GLOBAL. Stores BOTH annual and published-monthly figures (HMRC rounds the monthly value, e.g. ST £417, so it is not annual/12).';
COMMENT ON COLUMN payslips.tax_deducted_minor IS 'PAYE for the period. Can be negative when a cumulative tax code produces an in-year refund.';
