-- =============================================================================
-- BACKFILL: provision a chart of accounts for every organisation that has none.
-- One-off, for orgs created before per-org provisioning existed. Idempotent — only
-- touches orgs with zero categories, and ON CONFLICT guards re-runs. Uses the global
-- fallback template (country_code NULL); when per-country templates are added, mirror
-- ProvisionCategoriesForOrg's country resolution here.
--
-- Apply AFTER chart_template.sql. psql "$DATABASE_URL" -f db/seeds/backfill_charts.sql
-- =============================================================================
INSERT INTO categories (organisation_id, nominal_code, name, account_type, api_group,
                        tax_reporting_name, allowable_for_tax, default_vat,
                        is_capital_asset, is_system_managed, is_user_subdivided)
SELECT o.id, t.nominal_code, t.name, t.account_type, t.api_group,
       t.tax_reporting_name, t.allowable_for_tax, t.default_vat,
       t.is_capital_asset, t.is_system_managed, t.is_user_subdivided
FROM organisations o
CROSS JOIN chart_template t
WHERE t.country_code IS NULL
  AND NOT EXISTS (SELECT 1 FROM categories c WHERE c.organisation_id = o.id)
ON CONFLICT (organisation_id, nominal_code) DO NOTHING;
