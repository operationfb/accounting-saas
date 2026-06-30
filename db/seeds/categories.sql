-- =============================================================================
-- SEED: categories  (the development org's Chart of Accounts)
--
-- The standard chart now lives in EXACTLY ONE place — chart_template
-- (db/seeds/chart_template.sql, the global default template). This seed PROVISIONS the
-- development stub org
--   00000000-0000-0000-0000-000000000001
-- from that template, using the same INSERT…SELECT path ProvisionCategoriesForOrg
-- (db/queries/categories.sql) runs for every real org. So the ~100-row chart (income,
-- COS, admin/payroll expenses, FX, debtors/creditors/VAT/bank/user control accounts,
-- equity, system) is defined once and merely applied to dev here — no more drift
-- between this file and the per-org provisioning path.
--
-- APPLY ORDER: run db/seeds/chart_template.sql FIRST (it seeds the template this reads).
-- Idempotent (ON CONFLICT). Apply with:  psql "$DATABASE_URL" -f db/seeds/categories.sql
-- =============================================================================

INSERT INTO categories (organisation_id, nominal_code, name, account_type, api_group,
                        tax_reporting_name, allowable_for_tax, default_vat,
                        is_capital_asset, is_system_managed, is_user_subdivided)
SELECT '00000000-0000-0000-0000-000000000001',
       t.nominal_code, t.name, t.account_type, t.api_group,
       t.tax_reporting_name, t.allowable_for_tax, t.default_vat,
       t.is_capital_asset, t.is_system_managed, t.is_user_subdivided
FROM chart_template t
WHERE t.country_code IS NULL          -- the global fallback (the FreeAgent UK chart)
ON CONFLICT (organisation_id, nominal_code) DO NOTHING;
