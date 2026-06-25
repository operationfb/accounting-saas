-- =============================================================================
-- VAT MODULE — DATABASE SCHEMA
-- Accounting SaaS Platform (UK-focused, HMRC MTD-ready)
-- PostgreSQL 15+
--
-- One table: vat_returns — the saved snapshot of a VAT return for one period. The
-- return is COMPUTED LIVE for previews (the engine reads the source domains); a row
-- is persisted here only when the user MARKS a return as filed (or, later, files it
-- online). Once persisted with a "submitted" filing_status, the row makes its period
-- a FILED PERIOD: the source domains (expenses, invoices, bills, …) refuse to edit or
-- delete any record dated inside it, so a filed return can never silently change.
--
-- Applied AFTER schema.sql + auth_schema.sql, so set_updated_at() (schema.sql) and the
-- organisations / users tables (auth_schema.sql) already exist.
-- =============================================================================

CREATE TABLE vat_returns (
    id                  UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    organisation_id     UUID NOT NULL REFERENCES organisations(id) ON DELETE CASCADE, -- tenant
    created_by_user_id  UUID REFERENCES users(id),                                    -- who filed it

    -- The period this return covers. period_key is the synthetic id used in URLs
    -- (the period-end date in v1; the real HMRC periodKey in Phase 2).
    period_start        DATE        NOT NULL,
    period_end          DATE        NOT NULL,
    period_key          VARCHAR(10) NOT NULL,
    accounting_basis    VARCHAR(20) NOT NULL,   -- 'invoice' | 'cash', the basis at compute time

    -- The 9 HMRC boxes, in pence (BIGINT). Boxes 1–5 are VAT amounts; 6–9 are net
    -- values (already rounded to whole pounds at compute time). Box5 is SIGNED.
    box1_vat_due_sales          BIGINT NOT NULL DEFAULT 0,
    box2_vat_due_acquisitions   BIGINT NOT NULL DEFAULT 0,
    box3_total_vat_due          BIGINT NOT NULL DEFAULT 0,
    box4_vat_reclaimed          BIGINT NOT NULL DEFAULT 0,
    box5_net_vat                BIGINT NOT NULL DEFAULT 0,
    box6_total_sales_ex_vat     BIGINT NOT NULL DEFAULT 0,
    box7_total_purchases_ex_vat BIGINT NOT NULL DEFAULT 0,
    box8_ec_dispatches_ex_vat   BIGINT NOT NULL DEFAULT 0,
    box9_ec_acquisitions_ex_vat BIGINT NOT NULL DEFAULT 0,

    -- Filing lifecycle (FreeAgent-aligned). v1 reaches 'unfiled' (default) and
    -- 'marked_as_filed' (the manual button); 'pending'/'filed'/'rejected' arrive with
    -- Phase-2 online filing. A "submitted" status (marked_as_filed / filed / pending)
    -- locks the period.
    filing_due_on       DATE,
    filing_status       VARCHAR(20) NOT NULL DEFAULT 'unfiled'
                        CHECK (filing_status IN ('unfiled','pending','rejected','filed','marked_as_filed')),
    filed_at            TIMESTAMPTZ,
    filed_reference     VARCHAR(50),

    -- Payment to HMRC. payment_amount_due_minor is SIGNED (+ owed / − refund);
    -- payment_status is NULL when nothing is owed (a refund or nil return).
    payment_due_on              DATE,
    payment_amount_due_minor    BIGINT,
    payment_status              VARCHAR(20)
                                CHECK (payment_status IS NULL OR payment_status IN ('unpaid','marked_as_paid','paid')),

    -- Phase 2 only (HMRC online submission); nullable now so no later migration.
    hmrc_processing_date    TIMESTAMPTZ,
    hmrc_charge_ref         VARCHAR(50),
    finalised               BOOLEAN,

    created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    deleted_at          TIMESTAMPTZ
);

-- One live return per (org, period). Backs the mark-filed UPSERT's ON CONFLICT.
CREATE UNIQUE INDEX uq_vat_returns_org_period
    ON vat_returns (organisation_id, period_start, period_end)
    WHERE deleted_at IS NULL;

-- The lock lookup: "is this date inside a SUBMITTED period for this org?". Partial so
-- it only carries the handful of filed rows.
CREATE INDEX idx_vat_returns_locked
    ON vat_returns (organisation_id, period_start, period_end)
    WHERE deleted_at IS NULL AND filing_status IN ('marked_as_filed','filed','pending');

CREATE TRIGGER trg_vat_returns_updated_at
    BEFORE UPDATE ON vat_returns
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

COMMENT ON TABLE vat_returns IS 'Saved snapshot of a VAT return for one period. Persisted on Mark as filed (or Phase-2 online filing). A "submitted" filing_status makes its period a FILED PERIOD that the source domains refuse to mutate records inside.';
COMMENT ON COLUMN vat_returns.payment_amount_due_minor IS 'Signed pence: positive = owed to HMRC, negative = refund due. Equals the signed Box 5.';
