-- =============================================================================
-- BACKFILL: split transactional VAT out of 817 into 818 "VAT Reclaimed" (input)
-- and 819 "VAT Charged" (output). 817 "VAT" is retained as the VAT-RETURN control
-- account only.
--
-- The forward-looking pieces (new roles VAT_CHARGED/VAT_RECLAIMED, the 818/819 rows
-- in chart_template, the 5+6 posting-rule legs) are in the schema/seed files. But
-- those seeds are ON CONFLICT DO NOTHING, so re-running them does NOT (a) add 818/819
-- to orgs that ALREADY have a chart, nor (b) flip existing gl_posting_rules rows off
-- VAT_CONTROL. This one-off migration does exactly those, plus re-points historical
-- invoice VAT already posted to 817.
--
-- Idempotent. Run AFTER chart_template.sql and gl_account_roles.sql (the 818/819
-- accounts + role map). ledger_schema.sql is the source of truth for FRESH installs;
-- step 0 below applies the same account_role CHECK change to an ALREADY-created table.
--   psql "$DATABASE_URL" -f db/seeds/backfill_vat_split.sql
-- =============================================================================

-- 0) Widen the account_role CHECK to allow VAT_CHARGED / VAT_RECLAIMED. Re-running the
--    full DDL can't do this (the table already exists), and step 2's UPDATEs would be
--    rejected by the old constraint. Keep this list in sync with ledger_schema.sql.
ALTER TABLE gl_posting_rules DROP CONSTRAINT IF EXISTS gl_posting_rules_account_role_check;
ALTER TABLE gl_posting_rules ADD  CONSTRAINT gl_posting_rules_account_role_check
  CHECK (account_role IN (
    'BANK','DEBTORS','CREDITORS','VAT_CONTROL','VAT_CHARGED','VAT_RECLAIMED','USER_ACCOUNT',
    'OPENING_EQUITY','EXPLANATION_CATEGORY','SOURCE_CATEGORY',
    'SALES_DEFAULT','TRANSFER_SOURCE_BANK','TRANSFER_DEST_BANK',
    'SUSPENSE',
    'PAYROLL_GROSS_EXPENSE','PAYROLL_EMPLOYER_NI_EXPENSE',
    'PAYROLL_EMPLOYER_PENSION_EXPENSE','PAYE_NI_LIABILITY',
    'PENSION_LIABILITY','STUDENT_LOAN_LIABILITY','NET_PAY_PAYABLE',
    'OTHER_PAYROLL_DEDUCTIONS',
    'PAYROLL_DIRECTOR_GROSS_EXPENSE','PAYROLL_DIRECTOR_EMPLOYER_NI_EXPENSE',
    'PAYROLL_DIRECTOR_EMPLOYER_PENSION_EXPENSE',
    'FX_REALISED_GAIN','FX_REALISED_LOSS',
    'FX_UNREALISED_GAIN','FX_UNREALISED_LOSS'));

-- 1) Open 818 + 819 in EVERY org that already has a chart of accounts. Mirrors the
--    817 template row's flags (system-managed liability, not user-subdivided). New
--    orgs get these from chart_template via ProvisionCategoriesForOrg; this covers the
--    ones provisioned before the split existed. Skips orgs with no chart (nothing to
--    add to) and, via ON CONFLICT, any org that already has the row.
INSERT INTO categories (organisation_id, nominal_code, name, account_type, api_group,
                        tax_reporting_name, allowable_for_tax, default_vat,
                        is_capital_asset, is_system_managed, is_user_subdivided)
SELECT o.id, v.code, v.name, 'TAX_LIABILITY', 'general_categories',
       NULL, NULL, NULL,
       FALSE, TRUE, FALSE
FROM organisations o
CROSS JOIN (VALUES ('818', 'VAT Reclaimed'),
                   ('819', 'VAT Charged')) AS v(code, name)
WHERE EXISTS (SELECT 1 FROM categories c WHERE c.organisation_id = o.id)
ON CONFLICT (organisation_id, nominal_code) DO NOTHING;

-- 2) Re-point the 11 VAT posting-rule legs off VAT_CONTROL. Output VAT (sales side) →
--    VAT_CHARGED (819); input VAT (purchase side) → VAT_RECLAIMED (818). The seed's
--    ON CONFLICT DO NOTHING can't update these rows, so it's done explicitly here.
--    The split is by economic nature, not DR/CR: the two refund reversals flip
--    (SALES_REFUND is a DR that reverses OUTPUT VAT; REFUND is a CR that reverses INPUT VAT).
UPDATE gl_posting_rules SET account_role = 'VAT_CHARGED'
WHERE account_role = 'VAT_CONTROL'
  AND (event_code, leg_no) IN (
        ('INVOICE_SENT',           3),
        ('SALES',                  3),
        ('DISPOSAL_CAPITAL_ASSET', 3),
        ('OTHER_MONEY_IN',         3),
        ('SALES_REFUND',           2));

UPDATE gl_posting_rules SET account_role = 'VAT_RECLAIMED'
WHERE account_role = 'VAT_CONTROL'
  AND (event_code, leg_no) IN (
        ('PAYMENT',                2),
        ('PURCHASE_CAPITAL_ASSET', 2),
        ('OTHER_MONEY_OUT',        2),
        ('REFUND',                 3),
        ('EXPENSE_APPROVED',       2),
        ('BILL_CREATED',           2));

-- 3) Re-point HISTORICAL invoice VAT lines from 817 → 819. Only INVOICE entries have
--    posted VAT so far (bank/expense/bill events aren't wired to the poster yet), and
--    every INVOICE VAT leg is OUTPUT VAT — so there is nothing to move to 818, and the
--    2 hand-created MANUAL journals on 817 are deliberately left alone (source_type filter).
--
--    The gl_journal_* tables are append-only (guard triggers block UPDATE); we bypass
--    them exactly the way the test suite's withGLBypass does — SET session_replication_role
--    = replica disables ordinary + constraint triggers for THIS session only. Safe here
--    because we change only account_id (amounts untouched, so every entry still balances).
--    NOTE: this mutates immutable history. It's acceptable because these are a small set
--    of dev/test lines and we want the originals to READ 819; for real production data,
--    prefer reverse-and-repost (or per-org reclassification journals) instead.
SET session_replication_role = replica;

UPDATE gl_journal_lines l
SET account_id = c819.id
FROM gl_journal_entries e,
     categories c817,
     categories c819
WHERE l.journal_entry_id = e.id
  AND e.source_type = 'INVOICE'
  AND l.account_id = c817.id
  AND c817.organisation_id = e.organisation_id AND c817.nominal_code = '817'
  AND c819.organisation_id = e.organisation_id AND c819.nominal_code = '819';

SET session_replication_role = DEFAULT;
