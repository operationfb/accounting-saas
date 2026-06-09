-- =============================================================================
-- EXPENSES MODULE — DATABASE SCHEMA
-- Accounting SaaS Platform (UK-focused, HMRC MTD-ready)
-- PostgreSQL 15+
--
-- Design principles:
--   1. Money values are stored as INTEGER (pence/minor units), never DECIMAL or
--      FLOAT. This avoids floating-point rounding errors entirely. £12.34 = 1234.
--      Your Go service layer (or Dinero.js on the frontend) converts to/from
--      minor units at the boundary. This is the industry-standard approach for
--      financial data.
--   2. Multi-tenancy: every table carries an `organisation_id` so the schema
--      supports multiple companies in one database. All queries must filter by
--      this column and it participates in every index.
--   3. Soft deletes: rows are never hard-deleted. `deleted_at` is set instead.
--      This preserves audit trails and makes reconciliation safe.
--   4. Audit trail: `created_at`, `updated_at`, and `created_by_user_id` are on
--      every core table.
--   5. The expense record is split into two tables:
--        - `expenses`           — the core financial/accounting record
--        - `expense_mileage`    — mileage-specific fields (only for mileage claims)
--      This avoids a wide table full of NULLs. A mileage claim has no
--      gross_value; a regular expense has no mileage. Keeping them separate
--      makes both easier to validate and query.
--   6. Attachments are in their own table so one expense can have multiple
--      documents (e.g. receipt + invoice). FreeAgent only supports one; we
--      improve on that here.
--   7. Recurrence is extracted into its own table. This is cleaner than
--      embedding recurrence fields on every expense row, and it lets you
--      query "all recurring templates" easily.
--   8. `expense_category` is a reference table that maps to Chart of Accounts
--      nominal codes. This replaces FreeAgent's opaque category URL approach.
-- =============================================================================


-- -----------------------------------------------------------------------------
-- EXTENSION: pgcrypto — used to generate UUIDs (gen_random_uuid())
-- We use UUID primary keys rather than serial integers. UUIDs are safe to
-- expose in URLs and APIs without leaking record counts or ordering.
-- -----------------------------------------------------------------------------
CREATE EXTENSION IF NOT EXISTS pgcrypto;


-- =============================================================================
-- REFERENCE / LOOKUP TABLES
-- These tables hold relatively static data that expenses point to.
-- =============================================================================

-- -----------------------------------------------------------------------------
-- expense_categories
-- Maps to Chart of Accounts nominal codes. Replaces FreeAgent's opaque
-- category URL. The `is_mileage` flag marks the special mileage category
-- that triggers the mileage sub-record requirement.
-- UK-specific: nominal_code follows standard UK CoA conventions.
-- -----------------------------------------------------------------------------
CREATE TABLE expense_categories (
    id              UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    organisation_id UUID        NOT NULL,               -- which company owns this category
    nominal_code    VARCHAR(20) NOT NULL,               -- e.g. '7400' for travel, '8200' for computer equipment
    name            VARCHAR(100) NOT NULL,              -- human-readable: 'Travel & Subsistence'
    description     TEXT,
    is_mileage      BOOLEAN     NOT NULL DEFAULT FALSE, -- TRUE for the special Mileage category
    is_capital_asset BOOLEAN    NOT NULL DEFAULT FALSE, -- TRUE triggers depreciation schedule requirement
    is_stock_purchase BOOLEAN   NOT NULL DEFAULT FALSE, -- TRUE requires stock_item_id
    is_active       BOOLEAN     NOT NULL DEFAULT TRUE,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now(),

    CONSTRAINT uq_category_nominal_code UNIQUE (organisation_id, nominal_code)
);

-- Index: most queries filter by organisation
CREATE INDEX idx_expense_categories_org ON expense_categories (organisation_id) WHERE is_active = TRUE;


-- -----------------------------------------------------------------------------
-- vat_rates
-- Stores VAT rate definitions over time. VAT rates change (e.g. COVID 5%
-- hospitality rate), so we store effective_from/to to allow historical
-- accuracy. Rate is stored as INTEGER basis points: 20% = 2000, 5% = 500.
-- -----------------------------------------------------------------------------
CREATE TABLE vat_rates (
    id              UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    organisation_id UUID        NOT NULL,
    name            VARCHAR(50) NOT NULL,               -- 'Standard Rate', 'Reduced Rate', 'Zero Rate', 'Exempt'
    rate_bps        INTEGER     NOT NULL,               -- basis points: 2000 = 20.00%, 500 = 5.00%
    effective_from  DATE        NOT NULL,
    effective_to    DATE,                               -- NULL means currently active
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_vat_rates_org ON vat_rates (organisation_id);


-- =============================================================================
-- CORE TABLES
-- =============================================================================

-- -----------------------------------------------------------------------------
-- expenses
-- The central table. Each row is one expense claim/receipt entered by a user.
--
-- MONEY COLUMNS (all INTEGER, stored in minor currency units i.e. pence):
--   gross_value_minor          — total amount in the expense currency (e.g. USD cents)
--   native_gross_value_minor   — same amount converted to the company's home currency (GBP pence)
--   vat_value_minor            — VAT amount in expense currency
--   native_vat_value_minor     — VAT amount in home currency
--   manual_vat_amount_minor    — manually-overridden reclaimable VAT (home currency only)
--                                Used when expense is foreign currency but VAT is still
--                                reclaimable from HMRC (you enter the GBP reclaimable amount)
--
-- Sign convention (matches FreeAgent):
--   NEGATIVE = money owed TO the employee (company owes the claimant)
--   POSITIVE = refund DUE FROM the employee (claimant owes the company)
--   Example: employee paid £50 out of pocket → gross_value_minor = -5000
-- -----------------------------------------------------------------------------
CREATE TABLE expenses (
    -- -------------------------------------------------------------------------
    -- Identity
    -- -------------------------------------------------------------------------
    id                      UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    organisation_id         UUID        NOT NULL,               -- tenant isolation
    user_id                 UUID        NOT NULL,               -- the claimant (FK to users table)
    created_by_user_id      UUID        NOT NULL,               -- who created the record (may differ from claimant)

    -- -------------------------------------------------------------------------
    -- Core expense fields
    -- -------------------------------------------------------------------------
    category_id             UUID        NOT NULL REFERENCES expense_categories(id),
    dated_on                DATE        NOT NULL,               -- date on the receipt/invoice
    description             TEXT        NOT NULL,               -- free-text description, required
    receipt_reference       VARCHAR(100),                       -- optional ref number from the receipt

    -- -------------------------------------------------------------------------
    -- Currency & money
    -- Note: all _minor columns store amounts as integer minor units (pence).
    -- currency is the ISO 4217 code of the expense (may differ from company native).
    -- native_currency is the company's home currency (almost always 'GBP' for UK).
    -- -------------------------------------------------------------------------
    currency                CHAR(3)     NOT NULL DEFAULT 'GBP', -- ISO 4217 e.g. 'GBP', 'USD', 'EUR'
    native_currency         CHAR(3)     NOT NULL DEFAULT 'GBP', -- company's base currency
    exchange_rate           NUMERIC(18,6),                      -- rate used to convert to native; NULL if same currency

    gross_value_minor       INTEGER     NOT NULL,               -- total in expense currency (pence/cents/etc)
    native_gross_value_minor INTEGER    NOT NULL,               -- total in company home currency (GBP pence)

    -- -------------------------------------------------------------------------
    -- VAT fields
    -- vat_rate_id points to the vat_rates table — this gives us a proper
    -- audit trail of which rate was applied, not just a raw percentage.
    -- vat_status mirrors HMRC's categories for VAT return reporting.
    -- -------------------------------------------------------------------------
    vat_rate_id             UUID        REFERENCES vat_rates(id), -- NULL = no VAT / exempt
    vat_rate_bps            INTEGER,                            -- snapshot of rate at time of entry (basis points)
    vat_value_minor         INTEGER     NOT NULL DEFAULT 0,     -- VAT amount in expense currency
    native_vat_value_minor  INTEGER     NOT NULL DEFAULT 0,     -- VAT amount in home currency
    manual_vat_amount_minor INTEGER,                            -- override: reclaimable VAT in home currency
                                                                -- used for foreign currency expenses where
                                                                -- rate-based calc doesn't apply
    vat_status  VARCHAR(20) NOT NULL DEFAULT 'TAXABLE'
                CHECK (vat_status IN ('TAXABLE','EXEMPT','OUT_OF_SCOPE')),

    -- EC/post-Brexit VAT status for MTD VAT return boxes
    -- 'UK_NON_EC' is the default for post-2021 GB expenses
    ec_status   VARCHAR(30) NOT NULL DEFAULT 'UK_NON_EC'
                CHECK (ec_status IN (
                    'UK_NON_EC',        -- standard UK domestic
                    'EC_GOODS',         -- pre-2021 only, or NI protocol
                    'EC_SERVICES',      -- pre-2021 only, or NI protocol
                    'REVERSE_CHARGE'    -- post-2021, for certain B2B services
                )),

    -- -------------------------------------------------------------------------
    -- Project rebilling
    -- An expense can be rebilled to a client project (shown on their invoice).
    -- rebill_type determines how the rebill price is calculated:
    --   'cost'    — rebill at original cost
    --   'markup'  — rebill at cost × rebill_factor (e.g. 1.15 = 15% markup)
    --   'price'   — rebill at a fixed price (rebill_factor is the price in minor units)
    -- -------------------------------------------------------------------------
    project_id              UUID,                               -- FK to projects table (if rebilling)
    rebill_type             VARCHAR(10)
                            CHECK (rebill_type IN ('cost','markup','price')),
    rebill_factor           NUMERIC(10,4),                      -- multiplier or fixed price
    rebilled_invoice_id     UUID,                               -- set once this expense is invoiced

    -- -------------------------------------------------------------------------
    -- Stock purchases
    -- Only populated when category is_stock_purchase = TRUE
    -- -------------------------------------------------------------------------
    stock_item_id           UUID,                               -- FK to stock_items table
    stock_item_description  TEXT,                               -- snapshot of stock item name at time of entry
    stock_quantity          NUMERIC(10,4),                      -- units purchased

    -- -------------------------------------------------------------------------
    -- Capital assets
    -- Only populated when category is_capital_asset = TRUE
    -- -------------------------------------------------------------------------
    capital_asset_id        UUID,                               -- FK to capital_assets table (set post-creation)

    -- -------------------------------------------------------------------------
    -- Property (for landlord company types)
    -- Only populated for organisations of type UkUnincorporatedLandlord
    -- -------------------------------------------------------------------------
    property_id             UUID,                               -- FK to properties table

    -- -------------------------------------------------------------------------
    -- Approval workflow (improvement over FreeAgent — not in their API)
    -- Useful for multi-user companies where managers approve expenses.
    -- -------------------------------------------------------------------------
    status      VARCHAR(20) NOT NULL DEFAULT 'DRAFT'
                CHECK (status IN (
                    'DRAFT',        -- saved but not submitted
                    'SUBMITTED',    -- submitted by claimant, awaiting approval
                    'APPROVED',     -- approved by manager
                    'REJECTED',     -- rejected, requires correction
                    'PAID'          -- reimbursed to claimant
                )),
    submitted_at            TIMESTAMPTZ,
    approved_at             TIMESTAMPTZ,
    approved_by_user_id     UUID,
    paid_at                 TIMESTAMPTZ,
    rejection_note          TEXT,                               -- reason for rejection

    -- -------------------------------------------------------------------------
    -- Document capture metadata (Phase 2 — Datamolino-style)
    -- These fields will be populated by the OCR/data-capture service.
    -- Storing them here from the start means no schema migration later.
    -- -------------------------------------------------------------------------
    ocr_confidence          NUMERIC(5,4),                       -- 0.0–1.0 confidence score from OCR
    ocr_processed_at        TIMESTAMPTZ,
    supplier_name           VARCHAR(200),                       -- extracted or manually entered supplier
    supplier_vat_number     VARCHAR(30),                        -- supplier's VAT reg number (important for VAT reclaim)
    invoice_number          VARCHAR(100),                       -- supplier's invoice/receipt number

    -- -------------------------------------------------------------------------
    -- Soft delete & audit
    -- -------------------------------------------------------------------------
    deleted_at              TIMESTAMPTZ,                        -- NULL = active; set to delete
    created_at              TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at              TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Indexes for common query patterns
CREATE INDEX idx_expenses_org_user    ON expenses (organisation_id, user_id)   WHERE deleted_at IS NULL;
CREATE INDEX idx_expenses_org_dated   ON expenses (organisation_id, dated_on)  WHERE deleted_at IS NULL;
CREATE INDEX idx_expenses_org_status  ON expenses (organisation_id, status)    WHERE deleted_at IS NULL;
CREATE INDEX idx_expenses_org_project ON expenses (organisation_id, project_id) WHERE project_id IS NOT NULL AND deleted_at IS NULL;
CREATE INDEX idx_expenses_category    ON expenses (category_id)                WHERE deleted_at IS NULL;
-- Partial index for the approval queue
CREATE INDEX idx_expenses_submitted   ON expenses (organisation_id, submitted_at) WHERE status = 'SUBMITTED' AND deleted_at IS NULL;
-- For updated_since filtering (sync/webhook use)
CREATE INDEX idx_expenses_updated_at  ON expenses (organisation_id, updated_at);


-- -----------------------------------------------------------------------------
-- expense_mileage
-- Stores mileage-specific fields. Only exists when expenses.category is_mileage = TRUE.
-- Separating this avoids polluting the main expenses table with 10+ NULLable columns
-- that only apply to ~5% of records.
--
-- HMRC AMAP (Approved Mileage Allowance Payment) rules:
--   Cars: 45p/mile for first 10,000 miles per tax year; 25p/mile thereafter
--   Motorcycles: 24p/mile flat
--   Bicycles: 20p/mile flat
-- These rates are stored in mileage_rate_snapshots (below), not hardcoded here.
-- -----------------------------------------------------------------------------
CREATE TABLE expense_mileage (
    id                      UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    expense_id              UUID        NOT NULL UNIQUE REFERENCES expenses(id) ON DELETE CASCADE,
                                                                -- UNIQUE: one mileage record per expense
                                                                -- ON DELETE CASCADE: clean up if expense deleted

    -- Journey details
    miles                   NUMERIC(10,3) NOT NULL,            -- distance in miles (HMRC uses miles, not km)
    journey_description     TEXT,                              -- optional: 'London office to Birmingham client'
    journey_from            VARCHAR(200),                      -- departure location
    journey_to              VARCHAR(200),                      -- destination

    -- Vehicle
    vehicle_type            VARCHAR(20) NOT NULL
                            CHECK (vehicle_type IN ('CAR','MOTORCYCLE','BICYCLE')),
    engine_type             VARCHAR(30)                        -- 'PETROL','DIESEL','LPG','ELECTRIC','ELECTRIC_HOME','ELECTRIC_PUBLIC'
                            CHECK (engine_type IN (
                                'PETROL','DIESEL','LPG',
                                'ELECTRIC',                    -- legacy / pre-Sep 2025
                                'ELECTRIC_HOME',               -- home charger (post-Sep 2025)
                                'ELECTRIC_PUBLIC'              -- public charger (post-Sep 2025)
                            )),
    engine_size             VARCHAR(20),                       -- 'UP_TO_1400CC','1401_2000CC','OVER_2000CC' etc.
                                                               -- NULL for bicycles or electric vehicles

    -- HMRC reclaim / rebill
    reclaim_mileage         BOOLEAN     NOT NULL DEFAULT TRUE, -- TRUE = claim at AMAP rate from HMRC
    initial_rate_ppm        INTEGER,                           -- rate in pence-per-mile for first 10k miles
                                                               -- snapshot at time of claim
    reduced_rate_ppm        INTEGER,                           -- rate in ppm above 10k miles threshold
    rebill_rate_ppm         INTEGER,                           -- rate to charge client if rebilling project

    -- Calculated reimbursement amount (in GBP pence)
    -- This is computed by the service layer based on rates + miles, then stored.
    reimbursement_minor     INTEGER,                           -- total AMAP reimbursement in pence

    -- VAT
    have_vat_receipt        BOOLEAN     NOT NULL DEFAULT FALSE, -- did claimant get a VAT fuel receipt?

    created_at              TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at              TIMESTAMPTZ NOT NULL DEFAULT now()
);


-- -----------------------------------------------------------------------------
-- expense_attachments
-- Stores metadata about files attached to an expense (receipts, invoices, photos).
-- Actual file bytes live in object storage (GCS bucket); this table stores the
-- metadata and the path/URL to find the file.
--
-- Improvement over FreeAgent: supports multiple attachments per expense.
-- Phase 2: the OCR pipeline will populate ocr_* fields on this record.
-- -----------------------------------------------------------------------------
CREATE TABLE expense_attachments (
    id                  UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    expense_id          UUID        NOT NULL REFERENCES expenses(id) ON DELETE CASCADE,
    organisation_id     UUID        NOT NULL,               -- denormalised for partitioning/security

    -- File metadata
    file_name           VARCHAR(255) NOT NULL,              -- original filename e.g. 'receipt_jan.pdf'
    content_type        VARCHAR(100) NOT NULL,              -- MIME type: 'image/jpeg', 'application/pdf'
    file_size_bytes     INTEGER     NOT NULL,               -- size in bytes (max 10MB enforced in app layer)
    storage_path        TEXT        NOT NULL,               -- GCS object path e.g. 'orgs/abc/expenses/xyz/receipt.pdf'
    storage_bucket      VARCHAR(100) NOT NULL,              -- GCS bucket name

    -- Display / description
    description         TEXT,                               -- user-supplied description of this document
    is_primary          BOOLEAN     NOT NULL DEFAULT FALSE, -- the "main" receipt (shown first in UI)

    -- Phase 2: OCR / data capture fields
    -- These will be set by the document processing pipeline
    ocr_status          VARCHAR(20) NOT NULL DEFAULT 'PENDING'
                        CHECK (ocr_status IN ('PENDING','PROCESSING','COMPLETE','FAILED','SKIPPED')),
    ocr_raw_text        TEXT,                               -- full extracted text from the document
    ocr_extracted_data  JSONB,                              -- structured fields extracted: date, amount, supplier, VAT number
    ocr_processed_at    TIMESTAMPTZ,

    -- Audit
    uploaded_by_user_id UUID        NOT NULL,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_expense_attachments_expense ON expense_attachments (expense_id);
CREATE INDEX idx_expense_attachments_org     ON expense_attachments (organisation_id);
-- Index for the OCR processing queue
CREATE INDEX idx_expense_attachments_ocr     ON expense_attachments (ocr_status, created_at) WHERE ocr_status IN ('PENDING','PROCESSING');


-- -----------------------------------------------------------------------------
-- expense_recurrence
-- Defines a recurring template for expenses (e.g. monthly software subscription).
-- When a recurrence is active, the service layer generates new expense rows
-- on the schedule. The parent expense row holds the template values;
-- child expenses are linked back via `parent_expense_id`.
--
-- This is cleaner than embedding recurrence fields directly on every expense row:
-- only recurring expenses have a recurrence record.
-- -----------------------------------------------------------------------------
CREATE TABLE expense_recurrence (
    id                  UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    expense_id          UUID        NOT NULL UNIQUE REFERENCES expenses(id) ON DELETE CASCADE,
                                                            -- the "template" expense that defines the values
    organisation_id     UUID        NOT NULL,

    frequency           VARCHAR(20) NOT NULL
                        CHECK (frequency IN (
                            'WEEKLY','TWO_WEEKLY','FOUR_WEEKLY',
                            'MONTHLY','TWO_MONTHLY',
                            'QUARTERLY','BIANNUALLY','ANNUALLY','TWO_YEARLY'
                        )),
    next_recurs_on      DATE        NOT NULL,               -- when the next copy will be created
    end_date            DATE,                               -- NULL = recur indefinitely
    is_active           BOOLEAN     NOT NULL DEFAULT TRUE,

    created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_expense_recurrence_next ON expense_recurrence (next_recurs_on) WHERE is_active = TRUE;


-- -----------------------------------------------------------------------------
-- expense_audit_log
-- Immutable log of every change to an expense. Critical for accounting software
-- — you must be able to show auditors what changed, when, and by whom.
--
-- `old_data` and `new_data` are JSONB snapshots of the row before and after
-- the change. This is filled by a PostgreSQL trigger (see below).
-- -----------------------------------------------------------------------------
CREATE TABLE expense_audit_log (
    id              BIGSERIAL   PRIMARY KEY,               -- sequential for ordering; no UUID needed
    expense_id      UUID        NOT NULL,                  -- no FK constraint — log must survive expense deletion
    organisation_id UUID        NOT NULL,
    changed_by      UUID        NOT NULL,                  -- user who made the change
    action          VARCHAR(10) NOT NULL
                    CHECK (action IN ('INSERT','UPDATE','DELETE')),
    old_data        JSONB,                                 -- row before change (NULL for INSERT)
    new_data        JSONB,                                 -- row after change (NULL for DELETE)
    changed_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Append-only: no updates or deletes on this table ever
CREATE INDEX idx_expense_audit_expense ON expense_audit_log (expense_id, changed_at);
CREATE INDEX idx_expense_audit_org     ON expense_audit_log (organisation_id, changed_at);


-- =============================================================================
-- TRIGGERS
-- =============================================================================

-- -----------------------------------------------------------------------------
-- Trigger: auto-update updated_at on expenses
-- Every time a row in `expenses` is modified, set updated_at to now().
-- This is more reliable than relying on the application layer to set it.
-- -----------------------------------------------------------------------------
CREATE OR REPLACE FUNCTION set_updated_at()
RETURNS TRIGGER AS $$
BEGIN
    -- NEW is the row being written; we just stamp the timestamp
    NEW.updated_at = now();
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER trg_expenses_updated_at
    BEFORE UPDATE ON expenses
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

CREATE TRIGGER trg_expense_mileage_updated_at
    BEFORE UPDATE ON expense_mileage
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

CREATE TRIGGER trg_expense_attachments_updated_at
    BEFORE UPDATE ON expense_attachments
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

CREATE TRIGGER trg_expense_recurrence_updated_at
    BEFORE UPDATE ON expense_recurrence
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();


-- =============================================================================
-- VIEWS
-- =============================================================================

-- -----------------------------------------------------------------------------
-- v_expenses_full
-- A convenience view joining expenses with their mileage sub-record and
-- category. Use this in your Go service layer for the "get expense" endpoint
-- so you don't repeat the JOIN logic everywhere.
-- Note: this is a plain view, not materialised — it's always fresh.
-- -----------------------------------------------------------------------------
CREATE VIEW v_expenses_full AS
SELECT
    e.id,
    e.organisation_id,
    e.user_id,
    e.created_by_user_id,
    e.dated_on,
    e.description,
    e.receipt_reference,
    e.invoice_number,
    e.supplier_name,
    e.supplier_vat_number,
    e.currency,
    e.native_currency,
    e.exchange_rate,
    e.gross_value_minor,
    e.native_gross_value_minor,
    e.vat_rate_bps,
    e.vat_value_minor,
    e.native_vat_value_minor,
    e.manual_vat_amount_minor,
    e.vat_status,
    e.ec_status,
    e.project_id,
    e.rebill_type,
    e.rebill_factor,
    e.rebilled_invoice_id,
    e.stock_item_id,
    e.stock_quantity,
    e.capital_asset_id,
    e.status,
    e.submitted_at,
    e.approved_at,
    e.approved_by_user_id,
    e.paid_at,
    -- Category fields
    ec.nominal_code         AS category_nominal_code,
    ec.name                 AS category_name,
    ec.is_mileage           AS category_is_mileage,
    ec.is_capital_asset     AS category_is_capital_asset,
    -- Mileage fields (NULL if not a mileage claim)
    em.miles,
    em.vehicle_type,
    em.engine_type,
    em.engine_size,
    em.reclaim_mileage,
    em.initial_rate_ppm,
    em.reduced_rate_ppm,
    em.rebill_rate_ppm,
    em.reimbursement_minor,
    -- Timestamps
    e.created_at,
    e.updated_at
FROM expenses e
JOIN expense_categories ec ON ec.id = e.category_id
LEFT JOIN expense_mileage em ON em.expense_id = e.id   -- LEFT JOIN: only present for mileage claims
WHERE e.deleted_at IS NULL;                             -- soft delete filter always applied


-- =============================================================================
-- COMMENTS
-- Self-documenting the schema for future developers and tooling.
-- =============================================================================
COMMENT ON TABLE expenses               IS 'Core expense claims/receipts. One row per expense event.';
COMMENT ON TABLE expense_mileage        IS 'Mileage-specific fields. One-to-one with expenses where category is mileage.';
COMMENT ON TABLE expense_attachments    IS 'Receipt/invoice files attached to an expense. Multiple per expense supported.';
COMMENT ON TABLE expense_recurrence     IS 'Recurrence schedule for repeating expenses (e.g. monthly subscriptions).';
COMMENT ON TABLE expense_audit_log      IS 'Immutable audit trail of all expense changes. Never delete rows from this table.';
COMMENT ON TABLE expense_categories     IS 'Chart of Accounts categories available for expenses. Maps to nominal codes.';
COMMENT ON TABLE vat_rates              IS 'VAT rate definitions with effective date ranges. Rates stored in basis points.';

COMMENT ON COLUMN expenses.gross_value_minor         IS 'Total amount in minor units of expense currency (e.g. pence for GBP). Negative = owed to claimant.';
COMMENT ON COLUMN expenses.native_gross_value_minor  IS 'Total amount in minor units of company home currency (GBP pence for UK companies).';
COMMENT ON COLUMN expenses.manual_vat_amount_minor   IS 'Override for reclaimable VAT on foreign currency expenses. In home currency minor units.';
COMMENT ON COLUMN expenses.ec_status                 IS 'VAT reporting classification for MTD VAT returns. EC_GOODS and EC_SERVICES only valid pre-2021 or for NI protocol.';
COMMENT ON COLUMN expense_attachments.storage_path   IS 'GCS object path within storage_bucket. Never store a full signed URL here — generate those on demand.';
COMMENT ON COLUMN expense_audit_log.id               IS 'BIGSERIAL not UUID — sequential ordering matters for audit logs.';
