-- =============================================================================
-- SEED: expense_categories
-- FreeAgent chart of accounts (expense categories), grouped.
--
-- Target organisation: the development stub org
--   00000000-0000-0000-0000-000000000001
--
-- nominal_code holds FreeAgent's REAL nominal codes (sourced from
-- GET /v2/categories), because the FreeAgent expense push maps a category to
-- https://api.freeagent.com/v2/categories/{nominal_code} VERBATIM (see
-- deploy-expensepush/workflows/freeagent-push.yaml). Capital-asset purchases are
-- FreeAgent SUB-ACCOUNTS, hence codes like '602-1'. nominal_code is VARCHAR, so the
-- leading-zero / dashed sub-account strings are fine and nothing parses it as int.
--
-- Grouping (category_group):
--   'COS'    — Cost of Sales
--   'ADMIN'  — Admin expenses
--   'ASSETS' — Assets and stock (capital purchases; is_capital_asset = TRUE)
--
-- This file is idempotent AND self-correcting:
--   1. A one-time migration UPDATEs rows that still carry the project's original
--      *invented* placeholder codes (COS 5000s, Admin 6000s, Assets 0050s) to the
--      real FreeAgent codes. Keyed by the OLD code (unique per org) — NOT by name,
--      because legacy placeholder categories can share a name (e.g. 'Motor
--      Expenses'). No-op on a fresh DB. (Safe to delete once every env is migrated.)
--   2. INSERT ... ON CONFLICT DO NOTHING seeds a fresh DB; no-op once migrated, so
--      re-running never duplicates rows.
--
-- Apply with:
--   psql "$DATABASE_URL" -f db/seeds/expense_categories.sql
-- =============================================================================

-- 1. One-time migration: invented placeholder code -> real FreeAgent code. -----
--    Keyed by old code (unique per org); runs only against the dev org's rows.
UPDATE expense_categories AS c
SET nominal_code = m.new_code
FROM (VALUES
  -- Cost of Sales
  ('5000', '102'), ('5001', '101'), ('5002', '104'), ('5003', '103'), ('5004', '150'),
  -- Assets (capital purchase sub-accounts)
  ('0050', '602-1'), ('0051', '602-2'), ('0052', '602-3'), ('0053', '602-4'), ('0054', '602-5'),
  -- Admin expenses
  ('6000', '285'), ('6001', '292'), ('6002', '288'), ('6003', '335'), ('6004', '278'),
  ('6005', '270'), ('6006', '269'), ('6007', '293'), ('6008', '273'), ('6009', '291'),
  ('6010', '290'), ('6011', '274'), ('6012', '283'), ('6013', '250'), ('6014', '271'),
  ('6015', '272'), ('6016', '276'), ('6017', '251'), ('6018', '289'), ('6019', '282'),
  ('6020', '277'), ('6021', '280'), ('6022', '268'), ('6023', '363'), ('6024', '359'),
  ('6025', '360'), ('6026', '372'), ('6027', '294'), ('6028', '364'), ('6029', '362'),
  ('6030', '373'), ('6031', '351'), ('6032', '350'), ('6033', '358'), ('6034', '361'),
  ('6035', '365'), ('6036', '366'), ('6037', '371')
) AS m(old_code, new_code)
WHERE c.organisation_id = '00000000-0000-0000-0000-000000000001'
  AND c.nominal_code = m.old_code;

-- 2. Fresh-DB seed (no-op once migrated above). -------------------------------

-- Cost of Sales -------------------------------------------------------------
INSERT INTO expense_categories (organisation_id, nominal_code, name, category_group) VALUES
  ('00000000-0000-0000-0000-000000000001', '102', 'Commission Paid',     'COS'),
  ('00000000-0000-0000-0000-000000000001', '101', 'Cost of Sales',       'COS'),
  ('00000000-0000-0000-0000-000000000001', '104', 'Equipment Hire',      'COS'),
  ('00000000-0000-0000-0000-000000000001', '103', 'Materials',           'COS'),
  ('00000000-0000-0000-0000-000000000001', '150', 'Subcontractor Costs', 'COS')
ON CONFLICT (organisation_id, nominal_code) DO NOTHING;

-- Assets and stock (capital purchases) — FreeAgent 602-x sub-accounts --------
INSERT INTO expense_categories (organisation_id, nominal_code, name, category_group, is_capital_asset) VALUES
  ('00000000-0000-0000-0000-000000000001', '602-1', 'Computer Equipment Purchase',    'ASSETS', TRUE),
  ('00000000-0000-0000-0000-000000000001', '602-2', 'Fixtures and Fittings Purchase', 'ASSETS', TRUE),
  ('00000000-0000-0000-0000-000000000001', '602-3', 'Motor Vehicle Purchase',         'ASSETS', TRUE),
  ('00000000-0000-0000-0000-000000000001', '602-4', 'Other Capital Asset Purchase',   'ASSETS', TRUE),
  ('00000000-0000-0000-0000-000000000001', '602-5', 'Land and Property Purchase',     'ASSETS', TRUE)
ON CONFLICT (organisation_id, nominal_code) DO NOTHING;

-- Admin expenses ------------------------------------------------------------
INSERT INTO expense_categories (organisation_id, nominal_code, name, category_group) VALUES
  ('00000000-0000-0000-0000-000000000001', '285', 'Accommodation and Meals',        'ADMIN'),
  ('00000000-0000-0000-0000-000000000001', '292', 'Accountancy Fees',               'ADMIN'),
  ('00000000-0000-0000-0000-000000000001', '288', 'Advertising and Promotion',      'ADMIN'),
  ('00000000-0000-0000-0000-000000000001', '335', 'Business Entertaining',          'ADMIN'),
  ('00000000-0000-0000-0000-000000000001', '278', 'Childcare Vouchers',             'ADMIN'),
  ('00000000-0000-0000-0000-000000000001', '270', 'Computer Hardware',              'ADMIN'),
  ('00000000-0000-0000-0000-000000000001', '269', 'Computer Software',              'ADMIN'),
  ('00000000-0000-0000-0000-000000000001', '293', 'Consultancy Fees',               'ADMIN'),
  ('00000000-0000-0000-0000-000000000001', '273', 'Internet & Telephone',           'ADMIN'),
  ('00000000-0000-0000-0000-000000000001', '291', 'Leasing Payments',               'ADMIN'),
  ('00000000-0000-0000-0000-000000000001', '290', 'Legal and Professional Fees',    'ADMIN'),
  ('00000000-0000-0000-0000-000000000001', '274', 'Mobile Phone',                   'ADMIN'),
  ('00000000-0000-0000-0000-000000000001', '283', 'Motor Expenses',                 'ADMIN'),
  ('00000000-0000-0000-0000-000000000001', '250', 'Office Costs',                   'ADMIN'),
  ('00000000-0000-0000-0000-000000000001', '271', 'Office Equipment',               'ADMIN'),
  ('00000000-0000-0000-0000-000000000001', '272', 'Other Computer Costs',           'ADMIN'),
  ('00000000-0000-0000-0000-000000000001', '276', 'Printing',                       'ADMIN'),
  ('00000000-0000-0000-0000-000000000001', '251', 'Rent',                           'ADMIN'),
  ('00000000-0000-0000-0000-000000000001', '289', 'Staff Entertaining',             'ADMIN'),
  ('00000000-0000-0000-0000-000000000001', '282', 'Staff Training',                 'ADMIN'),
  ('00000000-0000-0000-0000-000000000001', '277', 'Stationery',                     'ADMIN'),
  ('00000000-0000-0000-0000-000000000001', '280', 'Sundries',                       'ADMIN'),
  ('00000000-0000-0000-0000-000000000001', '268', 'Web Hosting',                    'ADMIN'),
  ('00000000-0000-0000-0000-000000000001', '363', 'Bank/Finance Charges',           'ADMIN'),
  ('00000000-0000-0000-0000-000000000001', '359', 'Books and Journals',             'ADMIN'),
  ('00000000-0000-0000-0000-000000000001', '360', 'Charitable Donations',           'ADMIN'),
  ('00000000-0000-0000-0000-000000000001', '372', 'Corporation Tax Penalty',        'ADMIN'),
  ('00000000-0000-0000-0000-000000000001', '294', 'Formation Costs',                'ADMIN'),
  ('00000000-0000-0000-0000-000000000001', '364', 'Insurance',                      'ADMIN'),
  ('00000000-0000-0000-0000-000000000001', '362', 'Interest Payable',               'ADMIN'),
  ('00000000-0000-0000-0000-000000000001', '373', 'PAYE/NI Penalty',                'ADMIN'),
  ('00000000-0000-0000-0000-000000000001', '351', 'Pension (Annuity)',              'ADMIN'),
  ('00000000-0000-0000-0000-000000000001', '350', 'Pension (Personal/Stakeholder)', 'ADMIN'),
  ('00000000-0000-0000-0000-000000000001', '358', 'Postage',                        'ADMIN'),
  ('00000000-0000-0000-0000-000000000001', '361', 'Subscriptions',                  'ADMIN'),
  ('00000000-0000-0000-0000-000000000001', '365', 'Travel',                         'ADMIN'),
  ('00000000-0000-0000-0000-000000000001', '366', 'Use Of Home',                    'ADMIN'),
  ('00000000-0000-0000-0000-000000000001', '371', 'VAT Penalty',                    'ADMIN')
ON CONFLICT (organisation_id, nominal_code) DO NOTHING;
