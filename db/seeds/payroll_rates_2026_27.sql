-- =============================================================================
-- PAYROLL RATES SEED — Tax year 2026/27 (6 Apr 2026 → 5 Apr 2027)
-- File: db/seeds/payroll_rates_2026_27.sql
--
-- GLOBAL statutory reference data for internal/payroll's engine. Sourced from the
-- gov.uk "Rates and thresholds for employers 2026 to 2027" page and (for the
-- Scottish bands) the Scottish Income Tax page.
--
--   ⚠️  VERIFY BEFORE PRODUCTION USE.
--   The income-tax/NI thresholds were legislated frozen through 2027/28, so these
--   carry over from prior years — but confirm against gov.uk. Only NI category A is
--   confirmed against our reference data (a £700/mo employee → £42.45 employer NI;
--   a £1,047/mo director → £0 while cumulative pay is under the £5,000 ST). The
--   other category letters' rates below should be re-checked against the gov.uk
--   per-category table; a category with no row here is REJECTED by the service
--   rather than silently mis-calculated.
--
-- Idempotent: ON CONFLICT DO UPDATE so re-running refreshes the figures in place.
-- Money is BIGINT pence; rates are basis points (2000 = 20%).
-- =============================================================================

-- --- Per-year config ---------------------------------------------------------
INSERT INTO payroll_tax_years (tax_year_start, label, employment_allowance_cap_minor, default_tax_code, period_count)
VALUES (2026, '2026/27', 1050000, '1257L', 12)  -- Employment Allowance cap £10,500
ON CONFLICT (tax_year_start) DO UPDATE SET
    label                          = EXCLUDED.label,
    employment_allowance_cap_minor = EXCLUDED.employment_allowance_cap_minor,
    default_tax_code               = EXCLUDED.default_tax_code,
    period_count                   = EXCLUDED.period_count;

-- --- PAYE bands (above the personal allowance; cumulative upper bounds) -------
-- rUK = England & Northern Ireland (no tax-code prefix); Wales (C) is identical today.
DELETE FROM payroll_paye_bands WHERE tax_year_start = 2026;
INSERT INTO payroll_paye_bands (tax_year_start, region, band_order, upper_threshold_minor, rate_bps) VALUES
    -- England & Northern Ireland
    (2026, 'rUK', 1,  3770000, 2000),   -- basic 20% up to £37,700
    (2026, 'rUK', 2, 12514000, 4000),   -- higher 40% up to £125,140
    (2026, 'rUK', 3,     NULL, 4500),   -- additional 45% above
    -- Wales (currently mirrors rUK)
    (2026, 'C',   1,  3770000, 2000),
    (2026, 'C',   2, 12514000, 4000),
    (2026, 'C',   3,     NULL, 4500),
    -- Scotland
    (2026, 'S',   1,   396700, 1900),   -- starter 19% up to £3,967
    (2026, 'S',   2,  1695600, 2000),   -- basic 20% up to £16,956
    (2026, 'S',   3,  3109200, 2100),   -- intermediate 21% up to £31,092
    (2026, 'S',   4,  6243000, 4200),   -- higher 42% up to £62,430
    (2026, 'S',   5, 12514000, 4500),   -- advanced 45% up to £125,140
    (2026, 'S',   6,     NULL, 4800);   -- top 48% above

-- --- NI thresholds (annual + published rounded monthly figures) ---------------
INSERT INTO payroll_ni_thresholds (
    tax_year_start,
    lel_annual_minor, lel_monthly_minor,
    pt_annual_minor,  pt_monthly_minor,
    st_annual_minor,  st_monthly_minor,
    uel_annual_minor, uel_monthly_minor
) VALUES (
    2026,
     670800,  55900,   -- LEL £6,708 / £559
    1257000, 104800,   -- PT  £12,570 / £1,048
     500000,  41700,   -- ST  £5,000 / £417  (NOTE: published £417, not £5,000/12)
    5027000, 418900    -- UEL £50,270 / £4,189
)
ON CONFLICT (tax_year_start) DO UPDATE SET
    lel_annual_minor  = EXCLUDED.lel_annual_minor,  lel_monthly_minor = EXCLUDED.lel_monthly_minor,
    pt_annual_minor   = EXCLUDED.pt_annual_minor,   pt_monthly_minor  = EXCLUDED.pt_monthly_minor,
    st_annual_minor   = EXCLUDED.st_annual_minor,   st_monthly_minor  = EXCLUDED.st_monthly_minor,
    uel_annual_minor  = EXCLUDED.uel_annual_minor,  uel_monthly_minor = EXCLUDED.uel_monthly_minor;

-- --- NI category rates -------------------------------------------------------
-- employee_main = rate PT→UEL, employee_upper = rate above UEL, employer = rate above ST.
-- (H/M/Z employer 0% is a v1 simplification — their relief actually runs only up to the
-- Upper Secondary Threshold, which isn't modelled yet. See BACKLOG.)
DELETE FROM payroll_ni_category_rates WHERE tax_year_start = 2026;
INSERT INTO payroll_ni_category_rates (tax_year_start, category_letter, employee_main_bps, employee_upper_bps, employer_bps) VALUES
    (2026, 'A', 800, 200, 1500),   -- standard (CONFIRMED)
    (2026, 'C',   0,   0, 1500),   -- over State Pension age: employee pays nothing
    (2026, 'J', 200, 200, 1500),   -- deferred (employee has another job)
    (2026, 'H', 800, 200,    0),   -- apprentice under 25 (employer 0% — see note)
    (2026, 'M', 800, 200,    0),   -- under 21 (employer 0% — see note)
    (2026, 'Z', 200, 200,    0);   -- under 21, deferred (employer 0% — see note)
