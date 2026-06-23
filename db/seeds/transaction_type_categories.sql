-- =============================================================================
-- SEED: transaction_type_categories  (the type -> category/account mapping)
-- GLOBAL reference data. Encodes the supplied Money IN / Money OUT tabs: for each
-- transaction type, which CoA accounts it offers (every category per type), and —
-- since each row resolves to a nominal — which account that type-category pair posts
-- to. A row targets EITHER a whole api_group (the broad types) OR a specific
-- nominal_code. company_type 'ALL' = every org type; else Ltd vs sole trader.
--
-- Labels follow the tabs where they differ from the CoA account name (e.g. 904 is
-- "Employer NI" in the CoA but offered as "Benefit in Kind" under Money Paid to User).
-- Entity-link types (Transfer / Bill / Invoice / Credit Note / HP) have NO rows —
-- their account comes from the linked entity.
--
-- Idempotent: ON CONFLICT DO NOTHING (uq_ttc = type, nominal, api_group, company_type).
-- Apply with:  psql "$DATABASE_URL" -f db/seeds/transaction_type_categories.sql
-- =============================================================================

-- PAYMENT + REFUND: all expense categories (whole groups) + the payable taxes. ---
INSERT INTO transaction_type_categories (transaction_type_code, api_group, display_order)
SELECT t.code, g.grp, g.ord
FROM (VALUES ('PAYMENT'), ('REFUND')) AS t(code),
     (VALUES ('cost_of_sales_categories', 1),
             ('admin_expenses_categories', 2)) AS g(grp, ord)
ON CONFLICT DO NOTHING;

INSERT INTO transaction_type_categories (transaction_type_code, nominal_code, display_label, display_order)
SELECT t.code, n.code, n.label, n.ord
FROM (VALUES ('PAYMENT'), ('REFUND')) AS t(code),
     (VALUES ('820','Corporation Tax', 10),
             ('817','VAT',             11),
             ('814','PAYE / NI',       12),
             ('824','VAT OSS',         13)) AS n(code, label, ord)
ON CONFLICT DO NOTHING;

-- SALES: every income category (the income_categories group; 001 + custom 002-049). -
INSERT INTO transaction_type_categories (transaction_type_code, api_group, display_order) VALUES
  ('SALES', 'income_categories', 1)
ON CONFLICT DO NOTHING;

-- Single-account types (fixed posting account). --------------------------------
INSERT INTO transaction_type_categories (transaction_type_code, nominal_code, display_label, display_order) VALUES
  ('SALES_REFUND',           '001', 'Sales',                   1),  -- money out reduces sales
  ('PURCHASE_CAPITAL_ASSET', '602', 'Capital Asset Additions', 1),
  ('DISPOSAL_CAPITAL_ASSET', '604', 'Capital Asset Disposal',  1)
ON CONFLICT DO NOTHING;

-- OTHER MONEY OUT: specific control accounts (tab labels). ----------------------
INSERT INTO transaction_type_categories (transaction_type_code, nominal_code, display_label, display_order) VALUES
  ('OTHER_MONEY_OUT', '815', 'Other Payroll Deductions',  1),
  ('OTHER_MONEY_OUT', '998', 'Payment from Contra Account',2),
  ('OTHER_MONEY_OUT', '796', 'Payment to Initial Creditor',3),
  ('OTHER_MONEY_OUT', '797', 'Payment to Other Creditor',  4),
  ('OTHER_MONEY_OUT', '813', 'Pension Creditor',           5)
ON CONFLICT DO NOTHING;

-- OTHER MONEY IN: specific accounts (tab labels). ------------------------------
INSERT INTO transaction_type_categories (transaction_type_code, nominal_code, display_label, display_order) VALUES
  ('OTHER_MONEY_IN', '681', 'Receipt from Initial Debtor',  1),
  ('OTHER_MONEY_IN', '682', 'Receipt from Other Debtor',    2),
  ('OTHER_MONEY_IN', '670', 'Share Premium',                3),
  ('OTHER_MONEY_IN', '051', 'Interest Received',            4),
  ('OTHER_MONEY_IN', '056', 'Refund of Other Tax Received', 5)
ON CONFLICT DO NOTHING;

-- MONEY PAID TO USER: Ltd vs sole trader options (links a user). ---------------
INSERT INTO transaction_type_categories (transaction_type_code, nominal_code, company_type, display_label, display_order) VALUES
  ('MONEY_PAID_TO_USER', '902', 'limited',     'Net Salary / Bonus',       1),
  ('MONEY_PAID_TO_USER', '904', 'limited',     'Benefit in Kind',          2),
  ('MONEY_PAID_TO_USER', '905', 'limited',     'Expense Payment',          3),
  ('MONEY_PAID_TO_USER', '908', 'limited',     'Dividend',                 4),
  ('MONEY_PAID_TO_USER', '907', 'limited',     'Director''s Loan Account',  5),
  ('MONEY_PAID_TO_USER', '907', 'sole_trader', 'Drawings',                 1),
  ('MONEY_PAID_TO_USER', '908', 'sole_trader', 'Expense Payment',          2)
ON CONFLICT DO NOTHING;

-- MONEY RECEIVED FROM USER: Ltd vs sole trader (links a user). -----------------
INSERT INTO transaction_type_categories (transaction_type_code, nominal_code, company_type, display_label, display_order) VALUES
  ('MONEY_RECEIVED_FROM_USER', '907', 'limited',     'Director''s Loan Account', 1),
  ('MONEY_RECEIVED_FROM_USER', '910', 'sole_trader', 'Capital Introduced',      1)
ON CONFLICT DO NOTHING;
