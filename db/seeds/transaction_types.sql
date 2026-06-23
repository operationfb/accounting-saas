-- =============================================================================
-- SEED: transaction_types  (the 18 bank-transaction explanation types)
-- GLOBAL reference data (no organisation_id) — the FreeAgent "Type" dropdown,
-- grouped Money Out / Money In (the supplied screenshot).
--
-- entity_link records what the type links to instead of / alongside a category:
--   BANK_ACCOUNT — Transfer to/from Another Account
--   USER         — Money Paid/Received to/from User
--   INVOICE / BILL / CREDIT_NOTE / HP_AGREEMENT — future-entity modules (the type
--                  is selectable now; its dedicated link column lands with that module)
--   CAPITAL_ASSET — Disposal links an asset (Purchase posts to a 602 category)
--   NONE         — a pure category explanation
--
-- Idempotent: ON CONFLICT (code) DO NOTHING.
-- Apply with:  psql "$DATABASE_URL" -f db/seeds/transaction_types.sql
-- =============================================================================

INSERT INTO transaction_types (code, name, direction, entity_link, display_order) VALUES
  -- Money OUT
  ('PAYMENT',                 'Payment',                        'out', 'NONE',          1),
  ('BILL_PAYMENT',            'Bill Payment',                   'out', 'BILL',          2),
  ('TRANSFER_TO_ACCOUNT',     'Transfer to Another Account',    'out', 'BANK_ACCOUNT',  3),
  ('MONEY_PAID_TO_USER',      'Money Paid to User',             'out', 'USER',          4),
  ('PURCHASE_CAPITAL_ASSET',  'Purchase of Capital Asset',      'out', 'NONE',          5),
  ('SALES_REFUND',            'Sales Refund',                   'out', 'NONE',          6),
  ('CREDIT_NOTE_REFUND',      'Credit Note Refund',             'out', 'CREDIT_NOTE',   7),
  ('HP_PAYMENT',              'Payment of HP Agreement',        'out', 'HP_AGREEMENT',  8),
  ('OTHER_MONEY_OUT',         'Other Money Out',                'out', 'NONE',          9),
  -- Money IN
  ('INVOICE_RECEIPT',         'Invoice Receipt',                'in',  'INVOICE',       1),
  ('SALES',                   'Sales',                          'in',  'NONE',          2),
  ('TRANSFER_FROM_ACCOUNT',   'Transfer from Another Account',  'in',  'BANK_ACCOUNT',  3),
  ('REFUND',                  'Refund',                         'in',  'NONE',          4),
  ('BILL_REFUND',             'Bill Refund',                    'in',  'BILL',          5),
  ('MONEY_RECEIVED_FROM_USER','Money Received from User',       'in',  'USER',          6),
  ('DISPOSAL_CAPITAL_ASSET',  'Disposal of Capital Asset',      'in',  'CAPITAL_ASSET', 7),
  ('HP_REFUND',               'Refund of HP Agreement Payment', 'in',  'HP_AGREEMENT',  8),
  ('OTHER_MONEY_IN',          'Other Money In',                 'in',  'NONE',          9)
ON CONFLICT (code) DO NOTHING;
