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
  ('VAT_CONTROL',    '817'),   -- VAT-return control account (817). Reserved: no posting rule uses it after the 818/819 split — the future VAT-return process nets 818 + 819 into it.
  ('VAT_CHARGED',    '819'),   -- Output VAT charged on sales (INVOICE_SENT / SALES / DISPOSAL / OTHER_MONEY_IN / SALES_REFUND)
  ('VAT_RECLAIMED',  '818'),   -- Input VAT reclaimed on purchases (PAYMENT / PURCHASE_CAPITAL_ASSET / OTHER_MONEY_OUT / REFUND / EXPENSE_APPROVED / BILL_CREATED)
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
  -- Unrealised FX on the periodic revaluation of open foreign debtors. Single SIGNED
  -- account (391 "Unrealized Currency Exchange Gain/Loss"): gains CR it, losses DR it.
  -- Separate nominal from realised (390) so the two never double-count — the receipt
  -- crystallises realised in 390, the open-portion accrual lives in 391 until settled.
  ('FX_UNREALISED_GAIN', '391'),
  ('FX_UNREALISED_LOSS', '391'),
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

-- NOTE: the payroll P&L accounts (401–409) and the per-director is_user_subdivided
-- flag on 900–910 are now part of the consolidated default chart (db/seeds/chart_template.sql),
-- provisioned per org by ProvisionCategoriesForOrg — they used to be patched into the
-- dev-org categories here, but that fragmentation is gone. This file is now PURELY the
-- role → nominal map.
