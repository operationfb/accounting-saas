-- =============================================================================
-- BILLS MODULE — DATABASE SCHEMA
-- Accounting SaaS Platform (UK-focused, HMRC MTD-ready)
-- PostgreSQL 15+
--
-- A BILL is an accounts-PAYABLE record: a supplier's invoice that the
-- organisation owes and will pay. It is the payable twin of the INVOICES module
-- (a receivable) and structurally very close to an EXPENSE record, with a few
-- additions the FreeAgent "New Bill" screen shows: a supplier CONTACT, a DUE
-- date, free-text COMMENTS, a hire-purchase flag, and a project link.
--
-- This is the MINIMAL first cut (DATA LAYER ONLY — no service/handler/frontend
-- yet), exactly like the invoices module was introduced. Two tables:
--   - bills            — the record (header + the single spending line, flat)
--   - bill_attachments — the supplier-invoice files (metadata; bytes go to GCS)
--
-- Design decisions worth knowing:
--
--   ONE FLAT, SINGLE-LINE ROW (NOT header + line-items).
--   The "Bill Contents" on the form is ONE spending category + total + VAT rate,
--   so — like an expense — the body collapses straight onto the row (category_id,
--   vat_rate_bps, the *_value_minor totals). A multi-line bill_items child table
--   is deferred until a real multi-category bill is needed (see BACKLOG).
--
--   SPENDING CATEGORY → THE CHART OF ACCOUNTS (categories), NOT expense_categories.
--   A bill posts to a CoA nominal account (categories_schema.sql already earmarks
--   the CoA for "an invoice/bill/journal"). The picker is filtered to the SPENDING
--   subset (cost of sales / admin expense / capital asset) by the ListBillCategories
--   query so a bill can only point at a real spending account — the same accounts
--   the expense form offers.
--
--   MONEY IS INTEGER MINOR UNITS (PENCE), AND BIGINT — NOT INTEGER.
--   Every *_minor column is whole pence (£42.50 = 4250). BIGINT (like invoices /
--   projects), not the INTEGER used on a single expense, because a bill total can
--   exceed the int32 ceiling (~£21.4m). NEGATIVE values are allowed (a bill credit
--   note / refund, per the form note) — there is deliberately no positivity CHECK.
--   NEVER float/numeric for amounts that do arithmetic.
--
--   VAT BOTH WAYS (amounts_include_vat).
--   The form's "Bill totals will be entered: Including VAT / Excluding VAT" radio
--   is stored as amounts_include_vat. The (future) service computes the split with
--   money.ComputeFixedVAT (EXTRACT, when Including VAT) or money.AddOnVAT (ADD ON
--   TOP, when Excluding VAT) and persists net/sales_tax/total either way. The
--   form's "Auto" VAT rate is resolved to a concrete vat_rate_bps in the service
--   (from the category's default_vat) before it is stored.
--
--   STATUS IS THE STORED LIFECYCLE ONLY; THE DISPLAY STATUS IS DERIVED.
--   Like invoices, we store only the explicit states (DRAFT, OPEN, WRITTEN_OFF);
--   the payment-derived display (Open / Overdue / Paid / Overpaid) is computed at
--   read time from due_on + total_value_minor + paid_value_minor, so it never goes
--   stale. paid_value_minor exists precisely so that derivation has something to
--   read against (the reconciliation path that writes it is deferred — see BACKLOG).
--
--   NO AUTO-NUMBERING (unlike invoices).
--   A bill's `reference` is the SUPPLIER'S invoice number — we record it, we don't
--   generate it. So there is no next_bill_number counter and no Bump query (the
--   invoices' next_invoice_number machinery has no equivalent here). reference is
--   required at the app layer but is not a generated sequence and is not unique
--   (two different suppliers may reuse a number) in this cut.
--
--   MULTI-TENANCY + SOFT DELETE + AUDIT.
--   organisation_id scopes every row and leads the lookup index. deleted_at is a
--   soft delete (a bill is an audit record and gets referenced by payments later,
--   so it is never hard-removed). created_by_user_id records who entered it;
--   created_at/updated_at are stamped (updated_at by the shared set_updated_at() trigger).
--
--   ONE DOMAIN, ITS OWN SCHEMA FILE (like contacts / projects / invoices).
--   Generates into its own sqlc package (db/bills). Applied AFTER schema.sql,
--   auth_schema.sql, contacts_schema.sql, categories_schema.sql and
--   projects_schema.sql, so set_updated_at(), organisations, users, currencies,
--   contacts, categories and projects all already exist — the foreign keys below
--   are therefore declared INLINE (no deferred ALTER).
-- =============================================================================


-- -----------------------------------------------------------------------------
-- bills  (the record — header + single spending line, flat)
-- -----------------------------------------------------------------------------
CREATE TABLE bills (
    -- -------------------------------------------------------------------------
    -- Identity & tenancy
    -- -------------------------------------------------------------------------
    id                      UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    organisation_id         UUID NOT NULL REFERENCES organisations(id),   -- tenant that OWNS this bill
    created_by_user_id      UUID NOT NULL REFERENCES users(id),           -- who entered it (audit)
    contact_id              UUID NOT NULL REFERENCES contacts(id),        -- the SUPPLIER it's owed to (required)

    -- -------------------------------------------------------------------------
    -- Bill header
    -- -------------------------------------------------------------------------
    -- The supplier's invoice number. App-required (the form marks it required) but
    -- NOT auto-numbered and NOT unique here — two suppliers may reuse a number.
    reference               VARCHAR(100),
    dated_on                DATE NOT NULL,                  -- "Bill Date"
    -- "Due On" payment date. Nullable in the DB (so a future skeleton/Smart-Capture
    -- draft can exist before terms are set); required at the app layer.
    due_on                  DATE,
    -- ISO 4217 currency (FK to the global currencies table). Multi-currency
    -- conversion (exchange_rate + native_* totals) is deferred — see BACKLOG.
    currency                CHAR(3) NOT NULL DEFAULT 'GBP' REFERENCES currencies(code),
    comments                TEXT,                           -- free-text "Comments"
    -- "This will be paid using a hire purchase agreement". The actual HP-agreement
    -- entity + instalment schedule are deferred — this is just the flag for now.
    is_hire_purchase        BOOLEAN NOT NULL DEFAULT FALSE,

    -- -------------------------------------------------------------------------
    -- Bill contents — the single spending line, flat on the row (like an expense)
    -- -------------------------------------------------------------------------
    -- "Spending Category": a CoA nominal account. The ListBillCategories query
    -- filters the picker to the spending subset (cost of sales / admin / assets).
    category_id             UUID NOT NULL REFERENCES categories(id),
    -- The "Including VAT / Excluding VAT" radio. Drives whether the service
    -- EXTRACTS VAT from the total (TRUE) or ADDS it on top of the net (FALSE);
    -- either way net/sales_tax/total below are all persisted.
    amounts_include_vat     BOOLEAN NOT NULL DEFAULT TRUE,
    -- Resolved VAT rate in basis points (2000 = 20.00%, 500 = 5%, 0 = zero/no VAT),
    -- consistent with vat_rates.rate_bps. The form's "Auto" is resolved to a
    -- concrete value in the service (from categories.default_vat) before storing.
    vat_rate_bps            INTEGER NOT NULL DEFAULT 0,

    -- -------------------------------------------------------------------------
    -- Money (all BIGINT minor units / pence). Negative allowed = bill credit note.
    -- For a single-line bill the service computes net/sales_tax/total from the one
    -- line (total = "Total Price (including VAT)") and writes them here.
    -- -------------------------------------------------------------------------
    net_value_minor         BIGINT NOT NULL DEFAULT 0,      -- ex-VAT
    sales_tax_value_minor   BIGINT NOT NULL DEFAULT 0,      -- VAT amount
    total_value_minor       BIGINT NOT NULL DEFAULT 0,      -- gross = net + VAT
    paid_value_minor        BIGINT NOT NULL DEFAULT 0,      -- amount settled so far (drives derived Paid/Overdue)

    -- Outstanding amount. GENERATED so the database keeps it exactly = total - paid
    -- with no chance of drift; it is read-only (never written directly).
    due_value_minor         BIGINT GENERATED ALWAYS AS (total_value_minor - paid_value_minor) STORED,

    -- -------------------------------------------------------------------------
    -- Status — the STORED lifecycle only (display status is derived; see header note)
    --   DRAFT       — being entered, not yet a confirmed payable
    --   OPEN        — a live payable (the company owes it)
    --   WRITTEN_OFF — given up on / no longer payable
    -- VARCHAR + CHECK matches the enum style on invoices.status / expenses.status.
    -- -------------------------------------------------------------------------
    status                  VARCHAR(20) NOT NULL DEFAULT 'DRAFT'
                            CHECK (status IN ('DRAFT','OPEN','WRITTEN_OFF')),

    -- -------------------------------------------------------------------------
    -- Project link (optional) — "Link to Project". A real FK (the projects table
    -- is already loaded), unlike expenses.project_id which is a bare UUID.
    -- -------------------------------------------------------------------------
    project_id              UUID REFERENCES projects(id),

    -- -------------------------------------------------------------------------
    -- Lifecycle & audit
    -- -------------------------------------------------------------------------
    deleted_at              TIMESTAMPTZ,                    -- NULL = live; set to soft-delete
    created_at              TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at              TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Most queries filter by organisation and exclude soft-deleted rows. Partial so it
-- carries only live rows and stays small. Backs ListBills.
CREATE INDEX idx_bills_org ON bills (organisation_id) WHERE deleted_at IS NULL;

-- Backs ListBillsByContact and the contacts "in use" guard (ContactHasBills).
CREATE INDEX idx_bills_org_contact ON bills (organisation_id, contact_id) WHERE deleted_at IS NULL;

-- Backs "bills for a project". Partial on project_id IS NOT NULL so unlinked bills
-- (the common case) don't bloat the index.
CREATE INDEX idx_bills_org_project ON bills (organisation_id, project_id)
    WHERE deleted_at IS NULL AND project_id IS NOT NULL;


-- -----------------------------------------------------------------------------
-- bill_attachments
-- The supplier-invoice files attached to a bill (PDF/image). A direct clone of
-- expense_attachments: the file BYTES live in object storage (GCS); this table
-- holds the metadata + the storage key. bill_id CASCADE-deletes with the bill
-- (a hard delete; a soft delete of the bill leaves them, reached via the parent).
-- organisation_id is DENORMALISED (no FK, like expense_attachments) for org-scoped
-- attachment queries without joining the parent.
--
-- The ocr_* columns mirror the expense capture pipeline so the deferred bill Smart
-- Capture has its storage ready; the GCS upload + OCR WIRING is not part of this cut.
-- -----------------------------------------------------------------------------
CREATE TABLE bill_attachments (
    id                  UUID         PRIMARY KEY DEFAULT gen_random_uuid(),
    bill_id             UUID         NOT NULL REFERENCES bills(id) ON DELETE CASCADE,
    organisation_id     UUID         NOT NULL,               -- denormalised for org-scoped queries / security

    -- File metadata
    file_name           VARCHAR(255) NOT NULL,              -- original filename e.g. 'supplier_invoice.pdf'
    content_type        VARCHAR(100) NOT NULL,              -- MIME type: 'image/jpeg', 'application/pdf'
    file_size_bytes     INTEGER      NOT NULL,              -- size in bytes (max enforced in app layer)
    content_hash        VARCHAR(64),                        -- hex SHA-256 of the file bytes; dedupes a re-sent invoice. NULL for old rows.
    storage_path        TEXT         NOT NULL,              -- GCS object key e.g. 'orgs/abc/bills/xyz/invoice.pdf'
    storage_bucket      VARCHAR(100) NOT NULL,              -- GCS bucket name

    -- Display / description
    description         TEXT,                               -- "Attachment description" (user label for this file)
    is_primary          BOOLEAN      NOT NULL DEFAULT FALSE, -- the "main" document (shown first in UI)

    -- OCR / data capture (set by the future bill Smart-Capture pipeline)
    ocr_status          VARCHAR(20)  NOT NULL DEFAULT 'PENDING'
                        CHECK (ocr_status IN ('PENDING','PROCESSING','COMPLETE','FAILED','SKIPPED')),
    ocr_raw_text        TEXT,                               -- full extracted text from the document
    ocr_extracted_data  JSONB,                              -- structured fields: date, amount, supplier, VAT number
    ocr_processed_at    TIMESTAMPTZ,

    -- Audit
    uploaded_by_user_id UUID         NOT NULL,
    created_at          TIMESTAMPTZ  NOT NULL DEFAULT now(),
    updated_at          TIMESTAMPTZ  NOT NULL DEFAULT now()
);

CREATE INDEX idx_bill_attachments_bill ON bill_attachments (bill_id);
CREATE INDEX idx_bill_attachments_org  ON bill_attachments (organisation_id);
-- Content-dedupe lookup: "does this org already have an attachment with this hash?"
CREATE INDEX idx_bill_attachments_content_hash ON bill_attachments (organisation_id, content_hash) WHERE content_hash IS NOT NULL;
-- The OCR processing queue.
CREATE INDEX idx_bill_attachments_ocr  ON bill_attachments (ocr_status, created_at) WHERE ocr_status IN ('PENDING','PROCESSING');


-- =============================================================================
-- TRIGGERS — auto-update updated_at
-- Reuses the set_updated_at() function defined in db/schema/schema.sql, exactly
-- like contacts / projects / invoices do. Application order:
--   1. schema.sql            (defines set_updated_at, currencies, vat_rates)
--   2. auth_schema.sql       (organisations, users)
--   3. contacts_schema.sql   (contacts)
--   4. categories_schema.sql (categories)
--   5. projects_schema.sql   (projects)
--   6. bills_schema.sql      (this file)
-- =============================================================================
CREATE TRIGGER trg_bills_updated_at
    BEFORE UPDATE ON bills
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

CREATE TRIGGER trg_bill_attachments_updated_at
    BEFORE UPDATE ON bill_attachments
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();


-- =============================================================================
-- COMMENTS
-- =============================================================================
COMMENT ON TABLE  bills IS 'Accounts-payable bills (supplier invoices) an organisation owes. Org-scoped, soft-deleted. Single flat spending line (like an expense). Payable twin of invoices. Models the FreeAgent New Bill screen (minimal first cut).';
COMMENT ON COLUMN bills.contact_id IS 'The SUPPLIER (a contact) the bill is owed to. Required.';
COMMENT ON COLUMN bills.reference IS 'The SUPPLIER''S invoice number. App-required, but NOT auto-numbered (unlike invoices.reference) and NOT unique in this cut.';
COMMENT ON COLUMN bills.category_id IS 'Spending Category = a CoA (categories) nominal account. The picker is filtered to spending accounts by ListBillCategories.';
COMMENT ON COLUMN bills.amounts_include_vat IS 'The Including/Excluding-VAT radio. TRUE = service EXTRACTS VAT from the total (money.ComputeFixedVAT); FALSE = ADDS it on top of the net (money.AddOnVAT).';
COMMENT ON COLUMN bills.vat_rate_bps IS 'Resolved VAT rate in basis points (2000 = 20%). The form''s "Auto" is resolved from categories.default_vat in the service before storing.';
COMMENT ON COLUMN bills.total_value_minor IS 'Gross total (net + VAT) in minor units (pence). BIGINT because a bill total can exceed the int32 ceiling. Negative = bill credit note (refund).';
COMMENT ON COLUMN bills.due_value_minor IS 'Outstanding amount = total_value_minor - paid_value_minor. GENERATED/STORED so the DB keeps it correct; read-only.';
COMMENT ON COLUMN bills.status IS 'STORED lifecycle only: DRAFT|OPEN|WRITTEN_OFF. The display status (Open/Overdue/Paid/Overpaid) is DERIVED in the service from due_on + total_value_minor + paid_value_minor.';
COMMENT ON COLUMN bills.project_id IS 'Optional "Link to Project". A real FK to projects(id) (unlike expenses.project_id, which is a bare UUID).';
COMMENT ON TABLE  bill_attachments IS 'Supplier-invoice files for a bill (metadata; bytes in GCS). Clone of expense_attachments. Child of bills (ON DELETE CASCADE). ocr_* columns are ready for the deferred bill Smart-Capture pipeline.';
COMMENT ON COLUMN bill_attachments.storage_path IS 'GCS object key (never a signed URL — those are short-lived and generated on demand).';
COMMENT ON COLUMN bill_attachments.is_primary IS 'The main document for the bill (shown first). Exactly one primary per bill, kept coherent by UnsetBillPrimary + SetBillAttachmentPrimary in one transaction.';
