-- =============================================================================
-- SEED: categories  (the Chart of Accounts behind bank-transaction explanations)
-- Sourced from the FreeAgent Chart of Accounts workbook (UK Limited Company).
--
-- Target organisation: the development stub org
--   00000000-0000-0000-0000-000000000001
--
-- nominal_code holds FreeAgent's REAL nominal codes. This CoA is SEPARATE from
-- expense_categories (different code scheme) until a later unification.
--
-- Scope (per the increment): the SELECTABLE P&L categories (income, cost of sales,
-- ~58 admin expenses) PLUS every specific posting account the Money IN/OUT mapping
-- references (debtors, creditors, tax, user/director accounts, capital, contra).
-- Per-entity sub-accounts (750-x per bank, 900-x per user, 602-x per asset) are NOT
-- seeded — the explanation's entity links represent those.
--
-- account_type names follow the detailed P&L / Balance-Sheet sheets; where the
-- Money IN/OUT tabs use a friendlier label (e.g. 904 "BiK", 908 "Dividend") that
-- label lives on the MAPPING (transaction_type_categories.display_label), not here.
--
-- Idempotent: INSERT ... ON CONFLICT (organisation_id, nominal_code) DO NOTHING.
-- Apply with:  psql "$DATABASE_URL" -f db/seeds/categories.sql
-- =============================================================================

-- 1. INCOME (income_categories) — 001 Sales is FreeAgent's locked default income. --
INSERT INTO categories (organisation_id, nominal_code, name, account_type, api_group, default_vat)
SELECT '00000000-0000-0000-0000-000000000001', v.code, v.name, 'INCOME', 'income_categories', v.vat
FROM (VALUES
  ('001','Sales','STANDARD')
) AS v(code, name, vat)
ON CONFLICT (organisation_id, nominal_code) DO NOTHING;

-- 2. OTHER INCOME (general_categories) ----------------------------------------
INSERT INTO categories (organisation_id, nominal_code, name, account_type, api_group, default_vat)
SELECT '00000000-0000-0000-0000-000000000001', v.code, v.name, 'OTHER_INCOME', 'general_categories', v.vat
FROM (VALUES
  ('051','Interest Received',             'EXEMPT'),
  ('056','Refund of Other Tax Received',  'OUTSIDE_SCOPE')
) AS v(code, name, vat)
ON CONFLICT (organisation_id, nominal_code) DO NOTHING;

-- 3. COST OF SALES (cost_of_sales_categories) ---------------------------------
INSERT INTO categories (organisation_id, nominal_code, name, account_type, api_group, allowable_for_tax, default_vat)
SELECT '00000000-0000-0000-0000-000000000001', v.code, v.name, 'COST_OF_SALES', 'cost_of_sales_categories', TRUE, v.vat
FROM (VALUES
  ('100','Purchases',           'STANDARD'),
  ('101','Materials',           'STANDARD'),
  ('102','Commission Paid',     'STANDARD'),
  ('103','Subcontractor Costs', 'STANDARD')
) AS v(code, name, vat)
ON CONFLICT (organisation_id, nominal_code) DO NOTHING;

-- 4. ADMIN EXPENSES (admin_expenses_categories) — the ~58 defaults (200-258) ---
INSERT INTO categories (organisation_id, nominal_code, name, account_type, api_group, allowable_for_tax, default_vat)
SELECT '00000000-0000-0000-0000-000000000001', v.code, v.name, 'ADMIN_EXPENSE', 'admin_expenses_categories', v.allow, v.vat
FROM (VALUES
  ('200','Accountancy Fees',                TRUE,  'STANDARD'),
  ('201','Advertising',                     TRUE,  'STANDARD'),
  ('202','Bad Debts Written Off',           TRUE,  'OUTSIDE_SCOPE'),
  ('203','Bank Charges',                    TRUE,  'EXEMPT'),
  ('204','Business Entertaining',           FALSE, 'STANDARD'),
  ('205','Canteen',                         TRUE,  'STANDARD'),
  ('206','Charitable Donations',            FALSE, 'OUTSIDE_SCOPE'),
  ('207','Computer Software Costs',         TRUE,  'STANDARD'),
  ('208','Consumable Items',                TRUE,  'STANDARD'),
  ('209','Credit Card Charges',             TRUE,  'EXEMPT'),
  ('210','Depreciation of Fixed Assets',    FALSE, 'OUTSIDE_SCOPE'),
  ('211','Directors'' Pensions',            TRUE,  'OUTSIDE_SCOPE'),
  ('212','Directors'' Remuneration',        TRUE,  'OUTSIDE_SCOPE'),
  ('213','Employers NI (Directors)',        TRUE,  'OUTSIDE_SCOPE'),
  ('214','Employers NI (Staff)',            TRUE,  'OUTSIDE_SCOPE'),
  ('215','Finance Charges',                 TRUE,  'EXEMPT'),
  ('216','FX Transaction Charges',          TRUE,  'OUTSIDE_SCOPE'),
  ('217','Gain on Disposal of Assets',      FALSE, 'OUTSIDE_SCOPE'),
  ('218','Gain on FX Transactions',         FALSE, 'OUTSIDE_SCOPE'),
  ('219','General Consultancy Fees',        TRUE,  'STANDARD'),
  ('220','General Maintenance',             TRUE,  'STANDARD'),
  ('221','Hire/Lease - Computer Equipment', TRUE,  'STANDARD'),
  ('222','Hire/Lease - Motor Vehicles',     TRUE,  'STANDARD'),
  ('223','Hire/Lease - Other Assets',       TRUE,  'STANDARD'),
  ('224','IT and Computer Consumables',     TRUE,  'STANDARD'),
  ('225','Insurance',                       TRUE,  'EXEMPT'),
  ('226','Insurance on Premises',           TRUE,  'EXEMPT'),
  ('227','Interest Payable',                TRUE,  'EXEMPT'),
  ('228','Irrecoverable VAT',               TRUE,  'OUTSIDE_SCOPE'),
  ('229','Late Payment of Tax',             FALSE, 'OUTSIDE_SCOPE'),
  ('230','Leases and Hire Purchase',        TRUE,  'STANDARD'),
  ('231','Legal Fees',                      TRUE,  'STANDARD'),
  ('232','Management Fees',                 TRUE,  'STANDARD'),
  ('233','Other Legal & Professional Fees', TRUE,  'STANDARD'),
  ('234','Political Donations',             FALSE, 'OUTSIDE_SCOPE'),
  ('235','Postage Costs',                   TRUE,  'ZERO'),
  ('236','Premises Cleaning',               TRUE,  'STANDARD'),
  ('237','Premises Repairs & Maintenance',  TRUE,  'STANDARD'),
  ('238','Premises Repairs & Renewals',     TRUE,  'STANDARD'),
  ('239','Printing Costs',                  TRUE,  'STANDARD'),
  ('240','Publications & Subscriptions',    TRUE,  'STANDARD'),
  ('241','Rates on Premises',               TRUE,  'OUTSIDE_SCOPE'),
  ('242','Rent of Premises',                TRUE,  'STANDARD'),
  ('243','Research & Development',          TRUE,  'STANDARD'),
  ('244','Staff Benefits in Kind',          FALSE, 'OUTSIDE_SCOPE'),
  ('245','Staff Entertaining',              TRUE,  'STANDARD'),
  ('246','Staff Pensions',                  TRUE,  'OUTSIDE_SCOPE'),
  ('247','Staff Training',                  TRUE,  'STANDARD'),
  ('248','Staff Welfare',                   TRUE,  'STANDARD'),
  ('249','Stationery',                      TRUE,  'STANDARD'),
  ('250','Professional Body Subscriptions', TRUE,  'STANDARD'),
  ('251','Sundry Expenses',                 TRUE,  'STANDARD'),
  ('252','Telecommunication Costs',         TRUE,  'STANDARD'),
  ('253','Accommodation and Meals',         TRUE,  'STANDARD'),
  ('254','Travel and Subsistence',          TRUE,  'STANDARD'),
  ('255','Use of Residence',                TRUE,  'OUTSIDE_SCOPE'),
  ('256','Vehicle Running Costs',           TRUE,  'STANDARD'),
  ('257','Wages and Salaries',              TRUE,  'OUTSIDE_SCOPE'),
  ('258','Amortisation of Intangibles',     FALSE, 'OUTSIDE_SCOPE')
) AS v(code, name, allow, vat)
ON CONFLICT (organisation_id, nominal_code) DO NOTHING;

-- 5. CURRENT ASSETS (general_categories, system-managed control accounts) -----
INSERT INTO categories (organisation_id, nominal_code, name, account_type, api_group, is_system_managed)
SELECT '00000000-0000-0000-0000-000000000001', v.code, v.name, 'CURRENT_ASSET', 'general_categories', TRUE
FROM (VALUES
  ('681','Trade Debtors'),
  ('682','Other Debtors'),
  ('684','Prepayments and Accrued Income'),
  ('688','Stock / Work in Progress')
) AS v(code, name)
ON CONFLICT (organisation_id, nominal_code) DO NOTHING;

-- 6. CAPITAL ASSETS (general_categories; is_capital_asset) ---------------------
--    Parent additions / disposal accounts (per-asset sub-accounts are not seeded).
INSERT INTO categories (organisation_id, nominal_code, name, account_type, api_group, is_capital_asset)
SELECT '00000000-0000-0000-0000-000000000001', v.code, v.name, 'CAPITAL_ASSET', 'general_categories', TRUE
FROM (VALUES
  ('602','Capital Asset Additions'),
  ('604','Capital Asset Disposal')
) AS v(code, name)
ON CONFLICT (organisation_id, nominal_code) DO NOTHING;

-- 7. BANK (general_categories, system-managed) — parent; per-account is 750-x ---
INSERT INTO categories (organisation_id, nominal_code, name, account_type, api_group, is_system_managed)
SELECT '00000000-0000-0000-0000-000000000001', '750', 'Bank Account', 'BANK', 'general_categories', TRUE
ON CONFLICT (organisation_id, nominal_code) DO NOTHING;

-- 8. LIABILITIES (general_categories) -----------------------------------------
INSERT INTO categories (organisation_id, nominal_code, name, account_type, api_group, is_system_managed)
SELECT '00000000-0000-0000-0000-000000000001', v.code, v.name, 'LIABILITY', 'general_categories', v.sys
FROM (VALUES
  ('796','Trade Creditors',  TRUE),
  ('797','Other Creditors',  FALSE),
  ('813','Pension Creditor', TRUE)
) AS v(code, name, sys)
ON CONFLICT (organisation_id, nominal_code) DO NOTHING;

-- 9. TAX LIABILITIES (general_categories, system-managed) ----------------------
INSERT INTO categories (organisation_id, nominal_code, name, account_type, api_group, is_system_managed)
SELECT '00000000-0000-0000-0000-000000000001', v.code, v.name, 'TAX_LIABILITY', 'general_categories', TRUE
FROM (VALUES
  ('814','PAYE / NI'),
  ('815','Student Loans'),
  ('817','VAT'),
  ('820','Corporation Tax'),
  ('823','Deferred VAT'),
  ('824','VAT OSS')
) AS v(code, name)
ON CONFLICT (organisation_id, nominal_code) DO NOTHING;

-- 10. USER / DIRECTOR ACCOUNTS (general_categories, system-managed) ------------
--     Names per the Balance-Sheet sheet; the Money Paid/Received-to-User picker
--     relabels some of these (e.g. 904 "BiK", 908 "Dividend") via the mapping.
INSERT INTO categories (organisation_id, nominal_code, name, account_type, api_group, is_system_managed)
SELECT '00000000-0000-0000-0000-000000000001', v.code, v.name, 'USER_ACCOUNT', 'general_categories', TRUE
FROM (VALUES
  ('900','Capital Account'),
  ('902','Net Salary and Bonuses'),
  ('904','Employer NI'),
  ('905','Employer Pension'),
  ('907','Drawings / Money Paid to User'),
  ('908','Expense Payment'),
  ('910','Capital Introduced')
) AS v(code, name)
ON CONFLICT (organisation_id, nominal_code) DO NOTHING;

-- 11. EQUITY (general_categories) ---------------------------------------------
INSERT INTO categories (organisation_id, nominal_code, name, account_type, api_group, is_system_managed)
SELECT '00000000-0000-0000-0000-000000000001', v.code, v.name, 'EQUITY', 'general_categories', v.sys
FROM (VALUES
  ('670','Share Premium',    FALSE),
  ('921','Share Capital',    TRUE),
  ('968','Retained Profits', TRUE)
) AS v(code, name, sys)
ON CONFLICT (organisation_id, nominal_code) DO NOTHING;

-- 12. SYSTEM (general_categories, system-managed) -----------------------------
INSERT INTO categories (organisation_id, nominal_code, name, account_type, api_group, is_system_managed)
SELECT '00000000-0000-0000-0000-000000000001', v.code, v.name, 'SYSTEM', 'general_categories', TRUE
FROM (VALUES
  ('998','Money in Transit / Contra'),
  ('999','Suspense Account')
) AS v(code, name)
ON CONFLICT (organisation_id, nominal_code) DO NOTHING;
