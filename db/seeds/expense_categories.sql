-- =============================================================================
-- SEED: expense_categories
-- Standard FreeAgent-style chart of accounts (expense categories), grouped.
--
-- Target organisation: the development stub org
--   00000000-0000-0000-0000-000000000001
--
-- Grouping (category_group):
--   'COS'    — Cost of Sales
--   'ADMIN'  — Admin expenses
--   'ASSETS' — Assets and stock (capital purchases; is_capital_asset = TRUE)
--
-- Nominal codes are assigned in non-colliding ranges (the dev org's earlier
-- placeholder categories used 7400–8200): COS 5000s, Admin 6000s, Assets 0050s.
-- Within ADMIN, 6000–6022 are the "normally VATable" categories and 6023–6037
-- the "normally Zero-VAT" ones (we don't store the VAT flag, but the code order
-- preserves that split).
--
-- Idempotent: ON CONFLICT (organisation_id, nominal_code) DO NOTHING, so this
-- file is safe to re-run. Apply with:
--   psql "$DATABASE_URL" -f db/seeds/expense_categories.sql
-- =============================================================================

-- Cost of Sales -------------------------------------------------------------
INSERT INTO expense_categories (organisation_id, nominal_code, name, category_group) VALUES
  ('00000000-0000-0000-0000-000000000001', '5000', 'Commission Paid',     'COS'),
  ('00000000-0000-0000-0000-000000000001', '5001', 'Cost of Sales',       'COS'),
  ('00000000-0000-0000-0000-000000000001', '5002', 'Equipment Hire',      'COS'),
  ('00000000-0000-0000-0000-000000000001', '5003', 'Materials',           'COS'),
  ('00000000-0000-0000-0000-000000000001', '5004', 'Subcontractor Costs', 'COS')
ON CONFLICT (organisation_id, nominal_code) DO NOTHING;

-- Assets and stock (capital purchases) --------------------------------------
INSERT INTO expense_categories (organisation_id, nominal_code, name, category_group, is_capital_asset) VALUES
  ('00000000-0000-0000-0000-000000000001', '0050', 'Computer Equipment Purchase',    'ASSETS', TRUE),
  ('00000000-0000-0000-0000-000000000001', '0051', 'Fixtures and Fittings Purchase', 'ASSETS', TRUE),
  ('00000000-0000-0000-0000-000000000001', '0052', 'Motor Vehicle Purchase',         'ASSETS', TRUE),
  ('00000000-0000-0000-0000-000000000001', '0053', 'Other Capital Asset Purchase',   'ASSETS', TRUE),
  ('00000000-0000-0000-0000-000000000001', '0054', 'Land and Property Purchase',     'ASSETS', TRUE)
ON CONFLICT (organisation_id, nominal_code) DO NOTHING;

-- Admin expenses ------------------------------------------------------------
-- 6000–6022: normally VATable
-- 6023–6037: normally Zero-VAT
INSERT INTO expense_categories (organisation_id, nominal_code, name, category_group) VALUES
  ('00000000-0000-0000-0000-000000000001', '6000', 'Accommodation and Meals',        'ADMIN'),
  ('00000000-0000-0000-0000-000000000001', '6001', 'Accountancy Fees',               'ADMIN'),
  ('00000000-0000-0000-0000-000000000001', '6002', 'Advertising and Promotion',      'ADMIN'),
  ('00000000-0000-0000-0000-000000000001', '6003', 'Business Entertaining',          'ADMIN'),
  ('00000000-0000-0000-0000-000000000001', '6004', 'Childcare Vouchers',             'ADMIN'),
  ('00000000-0000-0000-0000-000000000001', '6005', 'Computer Hardware',              'ADMIN'),
  ('00000000-0000-0000-0000-000000000001', '6006', 'Computer Software',              'ADMIN'),
  ('00000000-0000-0000-0000-000000000001', '6007', 'Consultancy Fees',               'ADMIN'),
  ('00000000-0000-0000-0000-000000000001', '6008', 'Internet & Telephone',           'ADMIN'),
  ('00000000-0000-0000-0000-000000000001', '6009', 'Leasing Payments',               'ADMIN'),
  ('00000000-0000-0000-0000-000000000001', '6010', 'Legal and Professional Fees',    'ADMIN'),
  ('00000000-0000-0000-0000-000000000001', '6011', 'Mobile Phone',                   'ADMIN'),
  ('00000000-0000-0000-0000-000000000001', '6012', 'Motor Expenses',                 'ADMIN'),
  ('00000000-0000-0000-0000-000000000001', '6013', 'Office Costs',                   'ADMIN'),
  ('00000000-0000-0000-0000-000000000001', '6014', 'Office Equipment',               'ADMIN'),
  ('00000000-0000-0000-0000-000000000001', '6015', 'Other Computer Costs',           'ADMIN'),
  ('00000000-0000-0000-0000-000000000001', '6016', 'Printing',                       'ADMIN'),
  ('00000000-0000-0000-0000-000000000001', '6017', 'Rent',                           'ADMIN'),
  ('00000000-0000-0000-0000-000000000001', '6018', 'Staff Entertaining',             'ADMIN'),
  ('00000000-0000-0000-0000-000000000001', '6019', 'Staff Training',                 'ADMIN'),
  ('00000000-0000-0000-0000-000000000001', '6020', 'Stationery',                     'ADMIN'),
  ('00000000-0000-0000-0000-000000000001', '6021', 'Sundries',                       'ADMIN'),
  ('00000000-0000-0000-0000-000000000001', '6022', 'Web Hosting',                    'ADMIN'),
  ('00000000-0000-0000-0000-000000000001', '6023', 'Bank/Finance Charges',           'ADMIN'),
  ('00000000-0000-0000-0000-000000000001', '6024', 'Books and Journals',             'ADMIN'),
  ('00000000-0000-0000-0000-000000000001', '6025', 'Charitable Donations',           'ADMIN'),
  ('00000000-0000-0000-0000-000000000001', '6026', 'Corporation Tax Penalty',        'ADMIN'),
  ('00000000-0000-0000-0000-000000000001', '6027', 'Formation Costs',                'ADMIN'),
  ('00000000-0000-0000-0000-000000000001', '6028', 'Insurance',                      'ADMIN'),
  ('00000000-0000-0000-0000-000000000001', '6029', 'Interest Payable',               'ADMIN'),
  ('00000000-0000-0000-0000-000000000001', '6030', 'PAYE/NI Penalty',                'ADMIN'),
  ('00000000-0000-0000-0000-000000000001', '6031', 'Pension (Annuity)',              'ADMIN'),
  ('00000000-0000-0000-0000-000000000001', '6032', 'Pension (Personal/Stakeholder)', 'ADMIN'),
  ('00000000-0000-0000-0000-000000000001', '6033', 'Postage',                        'ADMIN'),
  ('00000000-0000-0000-0000-000000000001', '6034', 'Subscriptions',                  'ADMIN'),
  ('00000000-0000-0000-0000-000000000001', '6035', 'Travel',                         'ADMIN'),
  ('00000000-0000-0000-0000-000000000001', '6036', 'Use Of Home',                    'ADMIN'),
  ('00000000-0000-0000-0000-000000000001', '6037', 'VAT Penalty',                    'ADMIN')
ON CONFLICT (organisation_id, nominal_code) DO NOTHING;
