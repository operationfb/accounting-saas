-- =============================================================================
-- INVOICES MODULE — DATABASE SCHEMA
-- Accounting SaaS Platform (UK-focused, HMRC MTD-ready)
-- PostgreSQL 15+
--
-- A sales INVOICE an organisation issues to one of its contacts, modelled on the
-- FreeAgent Invoice resource (https://dev.freeagent.com/docs/invoices). Two
-- tables, in the standard parent/child shape:
--   - invoices       — the header (who it's for, dates, currency, totals, status)
--   - invoice_items  — the line items (one row per line on the invoice)
--
-- This is the MINIMAL first cut (datalayer only — no service/handler yet). It
-- carries just enough to issue a basic invoice; the larger FreeAgent surface
-- (discount, CIS, second sales tax, payment methods, recurring, multi-currency
-- conversion, per-line category/project, the display/email flags, …) is
-- deliberately deferred — see BACKLOG.md "Invoices".
--
-- Design decisions worth knowing:
--
--   ONE DOMAIN, ITS OWN SCHEMA FILE (like contacts / projects).
--   Generates into its own sqlc package (db/invoices). Applied AFTER schema.sql,
--   auth_schema.sql and contacts_schema.sql, so set_updated_at(), organisations,
--   users, currencies and contacts all already exist — the foreign keys below are
--   therefore declared INLINE (no deferred ALTER).
--
--   MONEY IS INTEGER MINOR UNITS (PENCE), AND BIGINT — NOT INTEGER.
--   Every *_minor column is whole pence (£42.50 = 4250). We use BIGINT, not the
--   INTEGER used on a single expense, because an invoice/billing total can exceed
--   the int32 ceiling (~£21.4m). This matches the money kernel's note (money/money.go)
--   and projects.billing_rate_pence / budget_money_pence (also BIGINT pence).
--   NEVER float/numeric for amounts that do arithmetic.
--
--   TOTALS ARE STORED, NOT JOINED ON EVERY READ.
--   net/sales_tax/total/paid are columns on the header, recomputed by the (future)
--   service from the line items on every save, so the invoice list/reporting never
--   has to re-aggregate the lines. due_value_minor is a GENERATED column
--   (total - paid) so the database keeps it correct for free.
--
--   STATUS IS THE STORED LIFECYCLE ONLY; THE DISPLAY STATUS IS DERIVED.
--   FreeAgent's visible statuses include time-derived (Overdue) and payment-derived
--   (Paid / Overpaid / Zero Value) ones that go stale if stored literally. So we
--   store ONLY the explicit, user-driven states here (DRAFT, SCHEDULED, SENT,
--   WRITTEN_OFF, REFUNDED) and the service derives Open/Overdue/Paid/Overpaid/Zero
--   at read time from due_on + total_value_minor + paid_value_minor. paid_value_minor
--   exists precisely so that derivation has something to read against.
--
--   MULTI-TENANCY + SOFT DELETE + AUDIT.
--   organisation_id scopes every row and leads the lookup index. deleted_at is a
--   soft delete (invoices are an audit record and get referenced by payments later,
--   so they're never hard-removed). created_by_user_id records who raised it;
--   created_at/updated_at are stamped (updated_at by the shared set_updated_at() trigger).
-- =============================================================================


-- -----------------------------------------------------------------------------
-- invoices  (the header)
-- -----------------------------------------------------------------------------
CREATE TABLE invoices (
    -- -------------------------------------------------------------------------
    -- Identity & tenancy
    -- -------------------------------------------------------------------------
    id                      UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    organisation_id         UUID NOT NULL REFERENCES organisations(id),  -- tenant that OWNS this invoice
    created_by_user_id      UUID NOT NULL REFERENCES users(id),          -- who raised it (audit)
    contact_id              UUID NOT NULL REFERENCES contacts(id),       -- who it's billed TO (required)

    -- -------------------------------------------------------------------------
    -- Core invoice fields
    -- -------------------------------------------------------------------------
    dated_on                DATE NOT NULL,                  -- the invoice date
    due_on                  DATE,                           -- payment due date; NULL until set/derived from terms
    -- The invoice number shown to the customer. Nullable because a DRAFT may not
    -- have a number assigned yet (auto-numbering is deferred — see BACKLOG). The
    -- partial UNIQUE index below makes it unique per org once it IS set.
    reference               VARCHAR(100),

    -- ISO 4217 currency the invoice is denominated in (FK to the global currencies
    -- table). Defaults to GBP. Conversion to the company's native currency
    -- (exchange_rate + native_* totals) is deferred — see BACKLOG.
    currency                CHAR(3) NOT NULL DEFAULT 'GBP' REFERENCES currencies(code),

    -- -------------------------------------------------------------------------
    -- Status — the STORED lifecycle only (display status is derived; see header note)
    --   DRAFT      — being edited, not yet issued
    --   SCHEDULED  — scheduled to be emailed/issued later
    --   SENT       — issued to the customer (a live receivable)
    --   WRITTEN_OFF— given up on as a bad debt
    --   REFUNDED   — money returned to the customer
    -- VARCHAR + CHECK matches the enum style used on expenses.status / contacts.charge_vat.
    -- -------------------------------------------------------------------------
    status                  VARCHAR(20) NOT NULL DEFAULT 'DRAFT'
                            CHECK (status IN ('DRAFT','SCHEDULED','SENT','WRITTEN_OFF','REFUNDED')),

    -- -------------------------------------------------------------------------
    -- Money (all BIGINT minor units / pence). The service RECOMPUTES net/sales_tax/
    -- total from the invoice_items on every save; paid is updated when a payment is
    -- recorded against the invoice (that path is deferred — see BACKLOG).
    -- -------------------------------------------------------------------------
    net_value_minor         BIGINT NOT NULL DEFAULT 0,      -- sum of line nets (ex-VAT)
    sales_tax_value_minor   BIGINT NOT NULL DEFAULT 0,      -- sum of line VAT
    total_value_minor       BIGINT NOT NULL DEFAULT 0,      -- gross = net + VAT
    paid_value_minor        BIGINT NOT NULL DEFAULT 0,      -- amount settled so far (drives derived Paid/Overpaid/Overdue)

    -- Outstanding amount. GENERATED so the database keeps it exactly = total - paid
    -- with no chance of drift; it is read-only (never written directly).
    due_value_minor         BIGINT GENERATED ALWAYS AS (total_value_minor - paid_value_minor) STORED,

    -- -------------------------------------------------------------------------
    -- Lifecycle & audit
    -- -------------------------------------------------------------------------
    deleted_at              TIMESTAMPTZ,                    -- NULL = live; set to soft-delete
    created_at              TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at              TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Most queries filter by organisation and exclude soft-deleted rows. Partial so it
-- carries only live rows and stays small. Backs ListInvoices.
CREATE INDEX idx_invoices_org ON invoices (organisation_id) WHERE deleted_at IS NULL;

-- Backs ListInvoicesByContact and the contacts "in use" guard (ContactHasInvoices).
CREATE INDEX idx_invoices_org_contact ON invoices (organisation_id, contact_id) WHERE deleted_at IS NULL;

-- One invoice NUMBER per organisation, once a number has actually been assigned.
-- Partial (reference IS NOT NULL) so any number of unnumbered DRAFTs can coexist;
-- (deleted_at IS NULL) so a soft-deleted invoice doesn't block reusing its number.
CREATE UNIQUE INDEX uq_invoices_reference ON invoices (organisation_id, reference)
    WHERE reference IS NOT NULL AND deleted_at IS NULL;


-- -----------------------------------------------------------------------------
-- invoice_items  (the lines)
-- One row per line on the invoice. Child of invoices via ON DELETE CASCADE (so a
-- hard delete of an invoice removes its lines; a SOFT delete leaves them, reached
-- only through the parent). organisation_id is DENORMALISED onto the line (like
-- expense_attachments) so item queries can be org-scoped defensively without
-- joining the parent.
--
-- We store the raw INPUTS only (quantity, unit price, VAT rate). The service
-- computes the line net (quantity × price_minor) and line VAT, and rolls them up
-- into the header totals above — there are no computed amount columns on the line
-- in this minimal cut.
-- -----------------------------------------------------------------------------
CREATE TABLE invoice_items (
    id                  UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    invoice_id          UUID NOT NULL REFERENCES invoices(id) ON DELETE CASCADE,
    organisation_id     UUID NOT NULL REFERENCES organisations(id),   -- denormalised for org-scoped item queries

    position            INTEGER NOT NULL,                 -- 1-based display order of the line
    description         TEXT NOT NULL,                    -- what the line is for

    -- Quantity is NUMERIC (not money) so it can be fractional (e.g. 2.5 hours).
    -- The numeric→string sqlc override surfaces it as a Go string; parse with
    -- shopspring/decimal in the service.
    quantity            NUMERIC(12,4) NOT NULL DEFAULT 0,

    -- Per-unit price in minor units (pence), VAT-EXCLUSIVE (net). BIGINT like the
    -- header totals. Line net = round(quantity × price_minor); VAT is then ADDED
    -- ON TOP at sales_tax_rate_bps (this is the opposite of the expenses path,
    -- which extracts VAT from a VAT-inclusive gross — do not reuse money.ComputeFixedVAT here).
    price_minor         BIGINT NOT NULL DEFAULT 0,

    -- VAT rate in basis points (2000 = 20.00%, 500 = 5%, 0 = zero-rated/no VAT),
    -- consistent with vat_rates.rate_bps.
    sales_tax_rate_bps  INTEGER NOT NULL DEFAULT 0,

    created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Fetch the lines for an invoice (ListInvoiceItems orders by position).
CREATE INDEX idx_invoice_items_invoice ON invoice_items (invoice_id);


-- =============================================================================
-- TRIGGERS — auto-update updated_at
-- Reuses the set_updated_at() function defined in db/schema/schema.sql, exactly
-- like contacts_schema.sql / projects_schema.sql do. Application order:
--   1. schema.sql           (defines set_updated_at, currencies)
--   2. auth_schema.sql      (organisations, users)
--   3. contacts_schema.sql  (contacts)
--   4. invoices_schema.sql  (this file)
-- =============================================================================
CREATE TRIGGER trg_invoices_updated_at
    BEFORE UPDATE ON invoices
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

CREATE TRIGGER trg_invoice_items_updated_at
    BEFORE UPDATE ON invoice_items
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();


-- =============================================================================
-- COMMENTS
-- =============================================================================
COMMENT ON TABLE  invoices IS 'Sales invoices an organisation issues to its contacts. Org-scoped, soft-deleted. Header for invoice_items. Models the FreeAgent Invoice resource (minimal first cut).';
COMMENT ON COLUMN invoices.status IS 'STORED lifecycle only: DRAFT|SCHEDULED|SENT|WRITTEN_OFF|REFUNDED. The display status (Open/Overdue/Paid/Overpaid/Zero Value) is DERIVED in the service from due_on + total_value_minor + paid_value_minor.';
COMMENT ON COLUMN invoices.total_value_minor IS 'Gross total (net + VAT) in minor units (pence). BIGINT because billing totals can exceed the int32 ceiling. Recomputed from invoice_items on save.';
COMMENT ON COLUMN invoices.due_value_minor IS 'Outstanding amount = total_value_minor - paid_value_minor. GENERATED/STORED so the DB keeps it correct; read-only.';
COMMENT ON COLUMN invoices.reference IS 'Customer-facing invoice number. Nullable until issued (auto-numbering deferred). Unique per org once set (uq_invoices_reference).';
COMMENT ON TABLE  invoice_items IS 'Line items of an invoice. Child of invoices (ON DELETE CASCADE). Stores raw inputs (quantity, unit price, VAT rate); the service computes line/header totals.';
COMMENT ON COLUMN invoice_items.price_minor IS 'Per-unit price in minor units (pence), VAT-EXCLUSIVE. VAT is added on top at sales_tax_rate_bps (NOT extracted — unlike the expenses path).';
COMMENT ON COLUMN invoice_items.quantity IS 'Line quantity, NUMERIC so it can be fractional (e.g. 2.5). Surfaced as a Go string (numeric override); parse with shopspring/decimal.';
