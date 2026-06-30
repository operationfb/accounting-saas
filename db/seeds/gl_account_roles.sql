-- =============================================================================
-- SEED: gl_account_roles  (fixed control role -> nominal_code)  + the per-user
-- subdivided-account flag on categories.
-- GLOBAL reference (no organisation_id) for the role map; the flag is per-org data.
--
-- The role map soft-links categories by nominal_code (resolved per org at post time,
-- like transaction_type_categories). Only the FIXED control roles are here; the
-- entity-derived roles (EXPLANATION_CATEGORY, BANK, …) resolve from the event's links.
--
-- USER_ACCOUNT -> 907 (Director's Loan Account): an out-of-pocket expense credits the
-- director's loan account (money the company owes them), and 907 is is_user_subdivided
-- so the resolver expands it per director. Nominals verified against FreeAgent's chart
-- by gl_posting_rules_freeagent_test.go.
--
-- Idempotent. Apply with:  psql "$DATABASE_URL" -f db/seeds/gl_account_roles.sql
-- =============================================================================

INSERT INTO gl_account_roles (role, nominal_code) VALUES
  ('DEBTORS',        '681'),   -- Trade Debtors
  ('CREDITORS',      '796'),   -- Trade Creditors
  ('VAT_CONTROL',    '817'),   -- VAT
  ('SALES_DEFAULT',  '001'),   -- Sales (until invoices carry per-line categories)
  ('OPENING_EQUITY', '968'),   -- provisional (FreeAgent "Profit and Loss" reserve)
  ('SUSPENSE',       '999'),   -- Suspense Account
  ('USER_ACCOUNT',   '907'),   -- Director's Loan Account (per-director; is_user_subdivided)
  ('BANK',           '750'),   -- Bank Account parent; expands per bank account (750-x)
  -- Realised FX on invoice receipts. Single SIGNED account (390 "Realized Currency
  -- Exchange Gain/Loss"): gains CR it, losses DR it — one net P&L line. Two roles (one per
  -- leg) both point at 390 so the poster's zero-leg drop keeps a same-currency receipt at
  -- 2 legs; split to separate gain/loss nominals later by repointing one role.
  ('FX_REALISED_GAIN', '390'),
  ('FX_REALISED_LOSS', '390'),
  -- Payroll accrual (PAYROLL_COMPLETED). Nominals are FreeAgent's ACTUAL payroll codes
  -- (confirmed against the live sandbox chart by gl_posting_rules_freeagent_test.go).
  -- The three employer-cost expense legs split by director status: STAFF → 401/402/403,
  -- DIRECTOR → 407/408/409 (chosen by the rule's employee_filter, keyed on
  -- payslips.nic_calculation). Liabilities + net pay are the same account for everyone.
  ('PAYROLL_GROSS_EXPENSE',                    '401'),  -- Salaries (staff, P&L)
  ('PAYROLL_EMPLOYER_NI_EXPENSE',              '402'),  -- Employer NICs (staff, P&L)
  ('PAYROLL_EMPLOYER_PENSION_EXPENSE',         '403'),  -- Staff Pensions (staff, P&L)
  ('PAYROLL_DIRECTOR_GROSS_EXPENSE',           '407'),  -- Directors' Salaries (P&L)
  ('PAYROLL_DIRECTOR_EMPLOYER_NI_EXPENSE',     '408'),  -- Directors' Employer NICs (P&L)
  ('PAYROLL_DIRECTOR_EMPLOYER_PENSION_EXPENSE','409'),  -- Directors' Staff Pensions (P&L)
  ('PAYE_NI_LIABILITY',                        '814'),  -- PAYE/NI owed to HMRC
  ('PENSION_LIABILITY',                        '813'),  -- Pension Creditor
  ('STUDENT_LOAN_LIABILITY',                   '815'),  -- Other Payroll Deductions (FreeAgent has no dedicated student-loan code)
  ('NET_PAY_PAYABLE',                          '902'),  -- Salary and Bonuses (per-employee; is_user_subdivided)
  ('OTHER_PAYROLL_DEDUCTIONS',                 '815')   -- Other Payroll Deductions
-- These are the GLOBAL defaults (organisation_id + country_code NULL). Per-org /
-- per-country overrides are inserted per deployment, not seeded here.
ON CONFLICT (role, organisation_id, country_code, company_type) DO NOTHING;

-- FreeAgent's payroll P&L expense accounts — our base CoA seed pre-dates these, so add
-- them where missing. account_type PAYROLL_EXPENSE; not offered in the explain picker
-- (api_group NULL). 401/402/403 = staff, 407/408/409 = director variants.
INSERT INTO categories (organisation_id, nominal_code, name, account_type, api_group, is_system_managed)
SELECT '00000000-0000-0000-0000-000000000001', v.code, v.name, 'PAYROLL_EXPENSE', NULL, FALSE
FROM (VALUES
  ('401','Salaries'),
  ('402','Employer NICs'),
  ('403','Staff Pensions'),
  ('407','Directors'' Salaries'),
  ('408','Directors'' Employer NICs'),
  ('409','Directors'' Staff Pensions')
) AS v(code, name)
ON CONFLICT (organisation_id, nominal_code) DO NOTHING;

-- Mark the per-user (per-director) accounts FreeAgent splits one row per director.
-- The WHOLE USER_ACCOUNT 900–910 set subdivides (Capital, Net Salary, Employer NI,
-- Expense Payment, DLA, Dividend, Capital Introduced). Per-org data; applies to every
-- org's chart on the shared dev DB. (For provisioning NEW orgs this flag should live
-- in db/seeds/categories.sql; see the design doc / BACKLOG.)
UPDATE categories
SET    is_user_subdivided = TRUE
WHERE  account_type = 'USER_ACCOUNT'
  AND  nominal_code BETWEEN '900' AND '910';
