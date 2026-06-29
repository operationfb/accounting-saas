-- =============================================================================
-- EXCHANGE RATES MODULE — DATABASE SCHEMA
-- Accounting SaaS Platform (UK-focused, HMRC MTD-ready)
-- PostgreSQL 15+
--
-- Daily foreign-exchange reference rates, used to:
--   - auto-fill an invoice's exchange_rate when it's raised in a foreign currency
--     (so the user doesn't hand-type a rate), and
--   - drive the periodic FX gain/loss revaluation of open receivables and foreign
--     bank balances (later phases).
--
-- Design decisions worth knowing:
--
--   GLOBAL REFERENCE DATA — NOT ORG-SCOPED.
--   A market exchange rate is the same for every tenant, exactly like the
--   `currencies` table (db/schema/schema.sql) this sits beside. So there is no
--   organisation_id, no soft delete, and no per-org row. The table is WRITTEN by a
--   background refresh (internal/fxrates pulls from a rate provider) and READ by
--   the invoices service + the revaluation job.
--
--   DIRECTION: HOME (GBP) PER 1 UNIT OF `currency`.
--   `rate` is how many units of the organisation's native/home currency one unit of
--   `currency` is worth (e.g. for EUR with rate 0.86, €1 = £0.86). This is the SAME
--   direction invoices already store in invoices.exchange_rate ("native per 1 unit
--   of the invoice currency"), so a looked-up rate drops straight into
--   money.ConvertMinor / the invoices nativeAmounts() path with no inversion.
--   The home currency itself (GBP) is never stored — its rate is implicitly 1.
--
--   ONE ROW PER (currency, rate_date), LAST-WRITE-WINS.
--   The PRIMARY KEY is the natural key, so a refresh UPSERTs (re-running a day's
--   fetch just overwrites it). NUMERIC(18,6) gives ample precision for a rate
--   without the float drift we never allow for money.
-- =============================================================================


CREATE TABLE exchange_rates (
    -- The foreign currency being quoted (FK to the global ISO 4217 table).
    currency   CHAR(3) NOT NULL REFERENCES currencies(code),

    -- The date this rate applies to. Lookups ask for "the rate on or before date D"
    -- (GetRateOnOrBefore) so a weekend/holiday with no published rate falls back to
    -- the most recent prior trading day.
    rate_date  DATE NOT NULL,

    -- HOME (GBP) units per 1 unit of `currency`. CHECK (> 0) so a bad/zero rate can
    -- never poison a conversion (a division/multiplication by zero downstream).
    rate       NUMERIC(18,6) NOT NULL CHECK (rate > 0),

    -- Where the rate came from (e.g. 'ecb' via Frankfurter). Kept for provenance /
    -- future multi-source reconciliation; not part of the key.
    source     VARCHAR(20) NOT NULL DEFAULT 'ecb',

    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),

    -- Natural key: one rate per currency per day. A re-fetch UPSERTs over it.
    PRIMARY KEY (currency, rate_date)
);

-- Backs GetRateOnOrBefore: "latest rate for this currency at/before a date". The PK
-- already indexes (currency, rate_date) in that order, so the DESC scan is cheap —
-- this explicit index documents the access pattern and keeps it fast if the PK ever
-- changes shape.
CREATE INDEX idx_exchange_rates_currency_date ON exchange_rates (currency, rate_date DESC);

COMMENT ON TABLE  exchange_rates IS 'Daily FX reference rates (global, not org-scoped). rate = HOME (GBP) units per 1 unit of `currency`, matching invoices.exchange_rate direction. Written by the fxrates refresh, read by invoices + the FX revaluation job.';
COMMENT ON COLUMN exchange_rates.rate IS 'HOME (GBP) units per 1 unit of `currency` (e.g. EUR 0.86 => €1 = £0.86). NUMERIC(18,6); CHECK (> 0). The home currency itself is never stored (implicitly 1).';
COMMENT ON COLUMN exchange_rates.rate_date IS 'Date the rate applies to. Lookups use "on or before" so non-trading days fall back to the most recent prior rate.';
