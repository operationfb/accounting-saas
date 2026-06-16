-- =============================================================================
-- CONTACTS MODULE — DATABASE SCHEMA
-- Accounting SaaS Platform (UK-focused, HMRC MTD-ready)
-- PostgreSQL 15+
--
-- A "contact" is a party an organisation does business with — a customer it
-- invoices and/or a supplier it buys from. It models the FreeAgent-style
-- "New Contact" screen: contact details (a person and/or a company), an
-- invoicing address, invoicing options (including the Charge VAT rule), and
-- optional bank details for paying that contact's bills.
--
-- Design decisions:
--
--   ONE TABLE, ITS OWN DOMAIN.
--   Contacts are their own bounded context (like auth, like expenses), so they
--   live in their own schema file and generate into their own sqlc package
--   (db/contacts). This file is applied AFTER schema.sql and auth_schema.sql,
--   so the set_updated_at() trigger function and the organisations/users tables
--   already exist — the foreign keys below are therefore declared INLINE (no
--   deferred ALTER like the expenses table needed).
--
--   "organisation_name" IS NOT "organisation_id" — READ THIS.
--   The form's "Organisation" field is the CONTACT's company name (e.g.
--   'Acme Ltd'). It is stored in `organisation_name`. The tenant that OWNS the
--   contact is `organisation_id` (FK to organisations) — the multi-tenancy
--   column every table carries. They are completely different things; do not
--   confuse them in queries or code.
--
--   PERMISSIVE NAMES.
--   The form says "Enter a first and last name, and/or an organisation name.
--   Both are not required." We honour that literally: first_name, last_name and
--   organisation_name are ALL nullable and there is NO cross-column CHECK forcing
--   at least one. The "a contact must have a name or an org name" rule is left to
--   the application layer (see BACKLOG.md) so edge cases (e.g. a contact known
--   only by email) are not rejected by the database.
--
--   MULTI-TENANCY + SOFT DELETE.
--   organisation_id scopes every row and leads the lookup index; deleted_at is a
--   soft delete (contacts get referenced by invoices later, so a contact is never
--   hard-removed). created_at/updated_at are stamped, updated_at by the shared
--   set_updated_at() trigger.
--
--   NO MONEY HERE.
--   Contacts hold no monetary amounts, so there are no _minor (pence) columns.
--   The only numeric field, default_payment_terms_days, is an integer COUNT OF
--   DAYS — and 0 ("Due on Receipt") is deliberately DISTINCT from NULL ("no
--   contact-level terms").
-- =============================================================================


CREATE TABLE contacts (
    -- -------------------------------------------------------------------------
    -- Identity & tenancy
    -- -------------------------------------------------------------------------
    id                  UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    organisation_id     UUID NOT NULL REFERENCES organisations(id),  -- tenant that OWNS this contact
    created_by_user_id  UUID NOT NULL REFERENCES users(id),          -- who created the record (audit)

    -- -------------------------------------------------------------------------
    -- Contact Details
    -- All three name fields are nullable (see "PERMISSIVE NAMES" above).
    -- -------------------------------------------------------------------------
    first_name          VARCHAR(100),
    last_name           VARCHAR(100),
    organisation_name   VARCHAR(200),   -- the CONTACT's company name — NOT organisation_id
    email               VARCHAR(320),   -- sized like users.email (RFC 5321 max length)
    billing_email       VARCHAR(320),   -- separate address invoices are sent to, if different
    telephone           VARCHAR(30),
    mobile              VARCHAR(30),

    -- -------------------------------------------------------------------------
    -- Invoicing Address
    -- Three free-form address lines plus structured town/region/postcode/country.
    -- -------------------------------------------------------------------------
    address_line_1      VARCHAR(255),
    address_line_2      VARCHAR(255),
    address_line_3      VARCHAR(255),
    town                VARCHAR(100),
    region              VARCHAR(100),   -- "Region or State"
    postcode            VARCHAR(20),    -- "Post/Zip code"
    -- ISO 3166-1 alpha-2 country code ('GB', 'DE', 'FR'). Stored as a code (not
    -- the display name) so it can drive the charge_vat = 'SAME_COUNTRY' rule by
    -- comparison with organisations.country_code. CHECK keeps it uppercase, like
    -- organisations.country_code / vat_rates.country_code.
    country_code        CHAR(2) NOT NULL DEFAULT 'GB'
                        CHECK (country_code = upper(country_code)),

    -- -------------------------------------------------------------------------
    -- Invoicing Options
    -- -------------------------------------------------------------------------
    -- Default invoice payment terms, in DAYS. Nullable on purpose:
    --   NULL = no contact-level terms (fall back to the org default)
    --   0    = "Due on Receipt"
    -- 0 and NULL are SEMANTICALLY DIFFERENT, so this column must never collapse
    -- 0 to NULL (the Go layer uses a 0-preserving helper, not pgNullInt32).
    default_payment_terms_days  INTEGER,

    uses_contact_level_email_settings    BOOLEAN NOT NULL DEFAULT FALSE,
    uses_contact_level_invoice_sequence  BOOLEAN NOT NULL DEFAULT FALSE,
    display_contact_name                 BOOLEAN NOT NULL DEFAULT TRUE,  -- form default: checked

    -- When to charge VAT to this contact. The three values map 1:1 to the form's
    -- dropdown: ALWAYS, NEVER, or SAME_COUNTRY ("only if the contact is in the
    -- same country/VAT area as us"). Defaults to SAME_COUNTRY, the form's selected
    -- option. VARCHAR + CHECK matches the enum style used on expenses.status etc.
    charge_vat          VARCHAR(20) NOT NULL DEFAULT 'SAME_COUNTRY'
                        CHECK (charge_vat IN ('ALWAYS','NEVER','SAME_COUNTRY')),

    vat_registration_number  VARCHAR(30),  -- the contact's VAT number, if shown on invoices

    -- Invoice/Estimate language. Stored as a short code (default 'en'); the UI
    -- maps codes to display names.
    invoice_language    VARCHAR(10) NOT NULL DEFAULT 'en',

    -- -------------------------------------------------------------------------
    -- Contact Bank Account Details
    -- Used to pay bills this contact sends us. UK-shaped (sort code + account
    -- number); kept as plain strings — these are not amounts that do arithmetic.
    -- -------------------------------------------------------------------------
    bank_sort_code      VARCHAR(8),     -- UK 6-digit sort code, often formatted '12-34-56'
    bank_account_number VARCHAR(20),    -- UK 8-digit account number (room left for variants)
    bank_recipient_name VARCHAR(200),   -- name on the account

    -- -------------------------------------------------------------------------
    -- Lifecycle & audit
    -- -------------------------------------------------------------------------
    is_active           BOOLEAN NOT NULL DEFAULT TRUE,   -- FALSE = hidden/archived contact
    deleted_at          TIMESTAMPTZ,                     -- NULL = active; set to soft-delete
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Most queries filter by organisation and exclude soft-deleted rows. This
-- partial index backs ListContacts and the per-id org-scoped lookups, and stays
-- small by carrying only live rows.
CREATE INDEX idx_contacts_org ON contacts (organisation_id) WHERE deleted_at IS NULL;


-- =============================================================================
-- TRIGGERS — auto-update updated_at
-- Reuses the set_updated_at() function defined in db/schema/schema.sql, exactly
-- like auth_schema.sql does. Schemas are applied in order:
--   1. schema.sql        (defines set_updated_at)
--   2. auth_schema.sql   (organisations, users)
--   3. contacts_schema.sql (this file)
-- =============================================================================
CREATE TRIGGER trg_contacts_updated_at
    BEFORE UPDATE ON contacts
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();


-- =============================================================================
-- COMMENTS
-- =============================================================================
COMMENT ON TABLE contacts IS 'Customers/suppliers an organisation invoices or buys from. Org-scoped, soft-deleted. Models the FreeAgent-style New Contact screen.';
COMMENT ON COLUMN contacts.organisation_name IS 'The CONTACT''s company name (e.g. ''Acme Ltd''). NOT organisation_id — that is the tenant that owns this contact.';
COMMENT ON COLUMN contacts.charge_vat IS 'When to charge VAT to this contact: ALWAYS | NEVER | SAME_COUNTRY (only if in the same country/VAT area). Defaults to SAME_COUNTRY.';
COMMENT ON COLUMN contacts.default_payment_terms_days IS 'Default invoice payment terms in DAYS. NULL = no contact-level terms; 0 = Due on Receipt. 0 and NULL are distinct.';
COMMENT ON COLUMN contacts.country_code IS 'ISO 3166-1 alpha-2, uppercase. Compared with organisations.country_code to apply the charge_vat = SAME_COUNTRY rule.';
