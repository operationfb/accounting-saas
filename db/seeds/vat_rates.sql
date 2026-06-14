-- =============================================================================
-- SEED: vat_rates
-- Statutory VAT rates, keyed by ISO 3166-1 alpha-2 country_code.
--
-- vat_rates is GLOBAL reference data (not per-organisation): the UK standard
-- rate is the same for every UK company, so rates are shared and an org selects
-- them via organisations.country_code.
--
-- Rate is stored in basis points: 20% = 2000, 5% = 500, 0% = 0.
--
-- is_fixed_ratio:
--   TRUE  — rate-locked: VAT amount must equal gross × rate (backend recomputes
--           and rejects mismatches). The norm for standard/reduced/zero rates.
--   FALSE — the user may enter a custom VAT amount (backend accepts it as-is).
--           Useful for receipts where the printed VAT doesn't exactly equal
--           gross × rate (e.g. per-line rounding across many items).
--
-- effective_from / effective_to define each rate's validity window. A rate with
-- effective_to in the past (e.g. the temporary COVID hospitality rate below) is
-- intentionally seeded so it is EXCLUDED by ListVatRatesByCountry today — it
-- exercises the date filtering and preserves historical accuracy.
--
-- Fixed UUIDs + ON CONFLICT (id) DO NOTHING make this idempotent (same pattern
-- as the dev org/user seed in auth_schema.sql). Apply with:
--   psql "$DATABASE_URL" -f db/seeds/vat_rates.sql
-- =============================================================================

-- United Kingdom (GB) -------------------------------------------------------
-- Currently-valid rates (effective_to NULL):
INSERT INTO vat_rates (id, country_code, name, rate_bps, is_fixed_ratio, effective_from, effective_to) VALUES
  ('10000000-0000-0000-0000-000000000001', 'GB', 'Standard Rate',          2000, TRUE,  '2011-01-04', NULL),
  ('10000000-0000-0000-0000-000000000002', 'GB', 'Reduced Rate',            500, TRUE,  '1997-09-01', NULL),
  ('10000000-0000-0000-0000-000000000003', 'GB', 'Zero Rate',                 0, TRUE,  '1973-04-01', NULL),
  -- Non-fixed-ratio example: 20% nominal, but the recorded VAT is taken from the
  -- receipt (user enters it, backend accepts) rather than computed from gross.
  ('10000000-0000-0000-0000-000000000004', 'GB', 'Standard Rate (manual)', 2000, FALSE, '2011-01-04', NULL),
  -- Expired example: the temporary COVID hospitality 12.5% rate. effective_to is
  -- in the past, so ListVatRatesByCountry must NOT return this today.
  ('10000000-0000-0000-0000-000000000005', 'GB', 'Hospitality (temporary)', 1250, TRUE, '2021-10-01', '2022-03-31')
ON CONFLICT (id) DO NOTHING;
