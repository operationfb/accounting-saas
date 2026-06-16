-- =============================================================================
-- PROJECTS MODULE — DATABASE SCHEMA
-- Accounting SaaS Platform (UK-focused, HMRC MTD-ready)
-- PostgreSQL 15+
--
-- A "project" is a unit of billable work for a contact. It carries time and
-- money budgets, a normal billing rate (per hour or per day), and several
-- options relevant to UK contractor/freelancer billing (IR35, invoice sequence,
-- unbillable time tracking).
--
-- Design decisions:
--
--   BUDGET STORED IN THREE TYPED COLUMNS.
--   The form lets the user pick a budget type: hours, days, or money.
--   Rather than squashing different units into one column, we use three
--   nullable columns — budget_hours_minutes, budget_days, budget_money_pence —
--   and a budget_type discriminator. Only the column matching the type is set;
--   the others are NULL. This keeps units explicit and avoids silent conversion
--   bugs (mixing pence with minutes in a single integer column is a footgun).
--
--   CONTACT FK, NOT TEXT.
--   contact_id is a proper foreign key to the contacts table. The form Contact*
--   field is a dropdown populated from GET /api/v1/contacts; the service layer
--   validates the contact belongs to the same org before inserting.
--
--   MONEY IN PENCE, TIME IN MINUTES.
--   billing_rate_pence and budget_money_pence are BIGINT (pence, minor units).
--   hours_per_day_minutes and budget_hours_minutes are INTEGER (minutes).
--   budget_days is INTEGER (whole days). All consistent with the rest of the
--   codebase.
--
--   HARD DELETE FOR NOW.
--   Projects have no invoices referencing them yet, so a simple DELETE is safe.
--   Add soft-delete (deleted_at) when projects are referenced by invoice lines.
-- =============================================================================

CREATE TABLE projects (
    -- -------------------------------------------------------------------------
    -- Identity & tenancy
    -- -------------------------------------------------------------------------
    id                          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    organisation_id             UUID NOT NULL REFERENCES organisations(id),
    contact_id                  UUID NOT NULL REFERENCES contacts(id),

    -- -------------------------------------------------------------------------
    -- Core project fields
    -- -------------------------------------------------------------------------
    name                        TEXT NOT NULL,
    status                      TEXT NOT NULL DEFAULT 'active'
                                    CHECK (status IN ('active','inactive','completed','cancelled')),
    contract_po_number          TEXT,
    -- When true, this project uses its own invoice number sequence rather than
    -- the org-wide sequence (a FreeAgent-style feature).
    project_invoice_sequence    BOOLEAN NOT NULL DEFAULT false,

    -- -------------------------------------------------------------------------
    -- Time and money
    -- -------------------------------------------------------------------------
    currency                    TEXT NOT NULL DEFAULT 'GBP',

    -- Budget discriminator: which of the three budget columns is active.
    -- NULL means "no budget set" (the form default shows 0 Hours but the user
    -- may leave it at zero to indicate no budget limit).
    budget_type                 TEXT CHECK (budget_type IN ('hours','days','money')),
    budget_hours_minutes        INTEGER,    -- total budget in minutes (budget_type='hours')
    budget_days                 INTEGER,    -- total budget in whole days (budget_type='days')
    budget_money_pence          BIGINT,     -- total budget in pence   (budget_type='money')

    -- Working day length in minutes (e.g. 480 for "8:00").
    -- Used to convert between hours and days on reports.
    hours_per_day_minutes       INTEGER,

    -- Normal billing rate in pence (minor units). 0 = not set.
    billing_rate_pence          BIGINT,
    billing_rate_unit           TEXT CHECK (billing_rate_unit IN ('per_hour','per_day')),
    -- When true, billing rate is shown as "plus VAT" on invoices.
    billing_rate_plus_vat       BOOLEAN NOT NULL DEFAULT true,

    -- -------------------------------------------------------------------------
    -- More options
    -- -------------------------------------------------------------------------
    -- True when this engagement is treated as employment under IR35 — affects
    -- how the contractor's income is taxed.
    is_ir35                     BOOLEAN NOT NULL DEFAULT false,
    start_date                  DATE,
    end_date                    DATE,
    -- When true, unbillable time logged against this project appears in the
    -- Project Profitability breakdown.
    include_unbillable_time     BOOLEAN NOT NULL DEFAULT true,

    -- -------------------------------------------------------------------------
    -- Lifecycle
    -- -------------------------------------------------------------------------
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);


-- =============================================================================
-- TRIGGERS
-- =============================================================================
CREATE TRIGGER set_projects_updated_at
    BEFORE UPDATE ON projects
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();


-- =============================================================================
-- INDEXES
-- =============================================================================
-- Most queries filter by organisation_id; this index keeps ListProjects fast.
CREATE INDEX idx_projects_organisation_id ON projects(organisation_id);


-- =============================================================================
-- COMMENTS
-- =============================================================================
COMMENT ON TABLE projects IS 'Billable work units for a contact. Org-scoped. Budget may be in hours, days, or money (see budget_type).';
COMMENT ON COLUMN projects.budget_type IS 'Discriminator for the three budget columns: hours | days | money. NULL = no budget limit.';
COMMENT ON COLUMN projects.budget_hours_minutes IS 'Budget in total MINUTES when budget_type=hours (e.g. 2400 for 40 hours).';
COMMENT ON COLUMN projects.billing_rate_pence IS 'Normal billing rate in pence (minor units). 0 = not set.';
COMMENT ON COLUMN projects.is_ir35 IS 'True when this engagement is treated as employment under IR35 — affects contractor tax treatment.';
COMMENT ON COLUMN projects.hours_per_day_minutes IS 'Working-day length in minutes (e.g. 480 for 8:00). Used to convert between hours and days.';
