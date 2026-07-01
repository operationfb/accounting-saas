-- =============================================================================
-- AUTH MODULE — DATABASE SCHEMA
-- Accounting SaaS Platform (UK-focused, HMRC MTD-ready)
-- PostgreSQL 15+
--
-- Tables in this file:
--   1. organisations          — the tenant/company entity
--   2. users                  — platform-level identity (a person who logs in)
--   3. organisation_memberships — links users to organisations with a role
--
-- Design decisions:
--
--   WHY THREE TABLES INSTEAD OF TWO?
--   A single "users" table with an organisation_id column would mean one user
--   can only ever belong to one company. That breaks real-world cases:
--     - An accountant who manages books for several clients
--     - A director sitting on the boards of multiple entities
--     - A bookkeeper working for a firm of 10 small businesses
--   The three-table model (users → memberships ← organisations) handles all
--   of these cleanly. The role (owner/admin/member) lives on the membership
--   row, not on the user, because the same person can be 'owner' at one
--   company and 'member' at another.
--
--   WHY IS password_hash ON users AND NOT ELSEWHERE?
--   Credentials are a property of the platform identity, not of any
--   particular organisation. You log in once as a user; your memberships
--   determine what you can see after that.
--
--   WHY store password_hash at all? Won't we use OAuth?
--   We start with email/password so the platform works without a third-party
--   dependency. OAuth (Google, Microsoft) can be added later as an additional
--   login method — the users table accommodates this via the nullable
--   password_hash (an OAuth-only user has no password).
--
--   WHY BCRYPT? WHY NOT ARGON2?
--   golang.org/x/crypto/bcrypt is the standard Go choice — battle-tested,
--   well-documented, and has a built-in cost factor for tuning work factor.
--   Argon2id is theoretically stronger but adds complexity. Bcrypt at cost 12
--   is the industry-standard minimum for a new application in 2024.
--   We never store the raw password — only the bcrypt hash.
--
--   MTD FIELDS ON organisations:
--   HMRC MTD VAT requires that when submitting VAT returns you identify the
--   business by its VRN (VAT Registration Number). We capture this here at
--   the organisation level rather than on individual VAT records. The MTD
--   OAuth tokens (access_token, refresh_token) that allow the app to call
--   HMRC's API on behalf of the business are also stored here — but those
--   are sensitive and will eventually be encrypted at rest.
-- =============================================================================


-- =============================================================================
-- 1. ORGANISATIONS
-- The tenant entity. Every other table's organisation_id points here.
-- One organisation = one registered company or sole trader.
-- =============================================================================

CREATE TABLE organisations (
    -- -------------------------------------------------------------------------
    -- Identity
    -- -------------------------------------------------------------------------
    id              UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    name            VARCHAR(200) NOT NULL,              -- company trading name e.g. 'Acme Ltd'
    slug            VARCHAR(100) UNIQUE,                -- URL-safe identifier e.g. 'acme-ltd'
                                                        -- used in future multi-tenant subdomains
                                                        -- NULL until explicitly set

    -- -------------------------------------------------------------------------
    -- UK company information
    -- These are optional — a sole trader won't have a companies house number.
    -- -------------------------------------------------------------------------
    companies_house_number  VARCHAR(20),                -- 8-digit UK Companies House number ("Company Registration Number" on the form)
    legal_name              VARCHAR(200),               -- registered legal name if different from trading name
    registered_address      TEXT,                       -- legacy free-text address; superseded by the
                                                        -- structured columns below (kept for back-compat)

    -- Legal form of the business. In the product this is set once at signup
    -- (changing it requires a fresh account), so it stays nullable here and is
    -- constrained by a CHECK rather than NOT NULL. Codes are snake_case; the
    -- frontend maps them to labels ("Limited Company", etc.).
    company_type            VARCHAR(40)
                            CHECK (company_type IS NULL OR company_type IN
                                  ('limited','sole_trader','partnership','landlord','corporation')),

    -- Structured registered/trading address — the Company Details screen edits
    -- these. Mirrors the contacts table's address columns for consistency.
    address_line_1          VARCHAR(200),
    address_line_2          VARCHAR(200),
    address_line_3          VARCHAR(200),
    town                    VARCHAR(100),
    region                  VARCHAR(100),               -- "Region or State"
    postcode                VARCHAR(20),

    -- -------------------------------------------------------------------------
    -- UK tax information
    -- -------------------------------------------------------------------------
    -- UTR = Unique Taxpayer Reference, assigned by HMRC to every UK taxpayer.
    -- Required for Self Assessment and Corporation Tax submissions. This is the
    -- form's "Corporation Tax Reference" (a.k.a. COTAX reference).
    utr                     VARCHAR(20),

    -- VAT Registration Number. Stored as the BARE 9 digits (e.g. '123456789'),
    -- no 'GB' prefix — that is exactly what HMRC's MTD VAT API expects as the {vrn}
    -- path segment, and what the VAT Registration form shows. NULL if the business
    -- is not VAT-registered. Edited on the VAT Registration screen (the VAT module),
    -- not Company Details — the Company Details PUT preserves it unchanged.
    vrn                     VARCHAR(20),

    -- PAYE employer reference (e.g. '120/RF11544') and the linked Accounts Office
    -- reference (e.g. '120PZ03790092'), used for payroll/RTI. Both optional — a
    -- sole trader with no employees won't have them.
    paye_reference              VARCHAR(20),
    accounts_office_reference   VARCHAR(20),

    -- Employment Allowance: whether the company claims the annual NI relief that
    -- offsets employer (secondary) Class 1 NI. Set on the Company Details screen;
    -- the payroll engine copies it onto each pay run and, when claimed, reduces the
    -- run's employer-NI liability by up to the annual cap. Eligibility is the user's
    -- declaration (HMRC rules vary year to year) — see the FreeAgent EA guide.
    claims_employment_allowance BOOLEAN     NOT NULL DEFAULT TRUE,

    -- -------------------------------------------------------------------------
    -- VAT registration settings (the "UK VAT Registration" screen, FreeAgent-style)
    -- Captured at the organisation level; the VAT-return calculation engine reads
    -- these (the accounting basis picks the calc path; the dates + frequency anchor
    -- the locally-generated period schedule). All nullable / defaulted because a
    -- not-yet-registered org has none of them. `vrn` (above) is the registration
    -- number for this same screen.
    -- -------------------------------------------------------------------------
    -- "Are you VAT Registered?" — a simple yes/no (the VAT registration itself).
    vat_registered              BOOLEAN     NOT NULL DEFAULT FALSE,

    -- "Do you need to use VAT rates other than standard UK ones?" (e.g. VAT MOSS or
    -- the domestic reverse charge). Stored now; the special-rate handling is deferred.
    vat_uses_non_standard_rates BOOLEAN     NOT NULL DEFAULT FALSE,

    -- Dates copied from the HMRC VAT registration certificate.
    --   vat_effective_date          — "Effective Date of VAT Registration" (when VAT
    --                                  liability began).
    --   vat_first_return_period_end — the end date of the FIRST VAT return; anchors
    --                                  the period schedule the return engine generates.
    vat_effective_date              DATE,
    vat_first_return_period_end     DATE,

    -- "Frequency of returns". CHECK mirrors the dropdown options.
    vat_return_frequency        VARCHAR(20)
                                CHECK (vat_return_frequency IS NULL OR vat_return_frequency IN
                                      ('monthly','quarterly','annually')),

    -- "VAT Accounting Basis" — invoice (accrual) vs cash. Selects which calculation
    -- path the return engine uses (both are supported).
    vat_accounting_basis        VARCHAR(20)
                                CHECK (vat_accounting_basis IS NULL OR vat_accounting_basis IN
                                      ('invoice','cash')),

    -- "Are you on the Flat Rate Scheme?" plus the flat-rate percentage stored as
    -- basis points (e.g. 10.5% = 1050), matching the bps convention used for every
    -- other rate in the schema. The flat-rate CALCULATION is deferred (see BACKLOG).
    vat_flat_rate_scheme        BOOLEAN     NOT NULL DEFAULT FALSE,
    vat_flat_rate_bps           INTEGER,

    -- "Include pre-registration expenses from" — how far back to pull purchases into
    -- the FIRST return, in MONTHS (NULL = don't include; 6 = last 6 months, for
    -- services; 48 = last 4 years, for goods still held). HMRC's pre-registration
    -- rules. The inclusion CALCULATION is deferred; this only records the choice.
    vat_pre_reg_expense_months  INTEGER,

    -- -------------------------------------------------------------------------
    -- Company contact details & business profile (Company Details screen)
    -- The "Other details" (shown on invoices/estimates) and "About your business"
    -- sections of the form. All optional.
    -- -------------------------------------------------------------------------
    business_phone          VARCHAR(30),                -- general business phone number
    contact_email           VARCHAR(320),               -- contact email shown on invoices/estimates
    contact_phone           VARCHAR(30),                -- contact phone shown on invoices/estimates
    website                 VARCHAR(255),
    business_category       VARCHAR(100),               -- e.g. 'Marketing & Advertising' (free text for now)
    business_description    TEXT,

    -- NOTE: HMRC MTD OAuth tokens are NOT stored here. They live per-(org,provider)
    -- in organisation_integrations (provider='hmrc'), the same table FreeAgent uses.
    -- (The old mtd_access_token / mtd_refresh_token / mtd_token_expires_at columns
    -- here were never wired up and were dropped.)

    -- -------------------------------------------------------------------------
    -- Billing / plan (stub — Phase 2 Stripe integration)
    -- We add these now so the column exists when we need it.
    -- They're nullable — a trial org has no Stripe customer yet.
    -- -------------------------------------------------------------------------
    -- Possible values: 'trial' | 'starter' | 'professional' | 'enterprise'
    -- A CHECK constraint keeps invalid values out of the database.
    plan                    VARCHAR(30) NOT NULL DEFAULT 'trial'
                            CHECK (plan IN ('trial','starter','professional','enterprise')),
    trial_ends_at           TIMESTAMPTZ,                -- NULL once on a paid plan
    stripe_customer_id      VARCHAR(100),               -- Stripe customer ID e.g. 'cus_abc123'
    stripe_subscription_id  VARCHAR(100),               -- Stripe subscription ID e.g. 'sub_abc123'

    -- -------------------------------------------------------------------------
    -- Locale / display preferences
    -- -------------------------------------------------------------------------
    -- ISO 4217 currency code. GBP for UK, but a UK company trading in USD
    -- might want USD as their native currency.
    native_currency         CHAR(3)     NOT NULL DEFAULT 'GBP' REFERENCES currencies(code),

    -- ISO 3166-1 alpha-2 country code the organisation belongs to: 'GB', 'DE',
    -- 'FR'. Determines which set of vat_rates (also keyed by country_code) apply.
    -- Defaults to 'GB' for this UK-focused MVP. CHECK keeps it uppercase.
    country_code            CHAR(2)     NOT NULL DEFAULT 'GB'
                            CHECK (country_code = upper(country_code)),

    -- IANA timezone. Used to display dates correctly and schedule recurring
    -- jobs at the right local time.
    timezone                VARCHAR(60) NOT NULL DEFAULT 'Europe/London',

    -- -------------------------------------------------------------------------
    -- Invoicing
    -- -------------------------------------------------------------------------
    -- The next sequential number for this org's "global" invoice sequence
    -- (FreeAgent-style). The invoices module pre-fills a new invoice's reference
    -- with this value, zero-padded (1 → '001'), and advances it by one when an
    -- invoice is created USING the suggested number. A manual override does NOT
    -- advance the counter unless it equals the current number — so accepting the
    -- default keeps a clean run while custom references are still allowed.
    next_invoice_number     INTEGER     NOT NULL DEFAULT 1,

    -- -------------------------------------------------------------------------
    -- Lifecycle
    -- -------------------------------------------------------------------------
    is_active               BOOLEAN     NOT NULL DEFAULT TRUE,
    created_at              TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at              TIMESTAMPTZ NOT NULL DEFAULT now(),
    deleted_at              TIMESTAMPTZ                         -- soft delete
);

-- Index: most platform queries check is_active
CREATE INDEX idx_organisations_active ON organisations (id) WHERE is_active = TRUE AND deleted_at IS NULL;

-- Index: slug lookups for subdomain routing
CREATE INDEX idx_organisations_slug ON organisations (slug) WHERE slug IS NOT NULL;

-- Comments
COMMENT ON TABLE organisations IS 'Tenant entity. One row per registered business. All other tables point to this via organisation_id.';
COMMENT ON COLUMN organisations.vrn IS 'VAT Registration Number, stored as the bare 9 digits (no GB prefix) — the form input and the HMRC MTD {vrn} path segment. NULL if not VAT-registered.';
COMMENT ON COLUMN organisations.utr IS 'HMRC Unique Taxpayer Reference. Required for Self Assessment / Corp Tax.';
COMMENT ON COLUMN organisations.country_code IS 'ISO 3166-1 alpha-2 country the org belongs to (e.g. GB). Selects the applicable vat_rates, which are keyed by the same country_code. NOT NULL, defaults to GB.';


-- =============================================================================
-- 2. USERS
-- Platform-level identity. A person who logs in.
-- One user can belong to multiple organisations via organisation_memberships.
-- =============================================================================

CREATE TABLE users (
    -- -------------------------------------------------------------------------
    -- Identity
    -- -------------------------------------------------------------------------
    id              UUID        PRIMARY KEY DEFAULT gen_random_uuid(),

    -- Email is the login identifier. Must be globally unique across the platform.
    -- We store it normalised to lowercase to prevent duplicate accounts.
    -- The UNIQUE constraint enforces one account per email address.
    email           VARCHAR(320) NOT NULL UNIQUE,       -- RFC 5321 max email length is 320 chars

    -- -------------------------------------------------------------------------
    -- Credentials
    -- password_hash is nullable because:
    --   - OAuth-only users (Google sign-in) never set a password
    --   - We can later allow SSO without breaking this table
    -- We store ONLY the bcrypt hash — never the plaintext password.
    -- In Go: golang.org/x/crypto/bcrypt.GenerateFromPassword([]byte(password), 12)
    -- bcrypt cost 12 is the recommended minimum as of 2024. Each +1 doubles
    -- the work factor. Revisit if login latency becomes an issue (cost 14 = ~1s).
    -- -------------------------------------------------------------------------
    password_hash   VARCHAR(72),                        -- bcrypt output is always 60 chars; 72 gives headroom

    -- -------------------------------------------------------------------------
    -- Profile
    -- -------------------------------------------------------------------------
    first_name      VARCHAR(100) NOT NULL,
    last_name       VARCHAR(100) NOT NULL,

    -- Phone is optional but useful for 2FA (Phase 2) and account recovery.
    phone           VARCHAR(30),

    -- Avatar URL points to a GCS object or an external provider URL.
    -- NULL = use generated initials avatar in the UI.
    avatar_url      TEXT,

    -- -------------------------------------------------------------------------
    -- Payroll identity
    -- Captured on the User Details screen; consumed by the future payroll module.
    -- All nullable: existing accounts have none, and a user is usable without them.
    -- Format is validated in the service layer (see internal/kernel), not the DB,
    -- so an import or a partially-filled form is never rejected at the column.
    -- -------------------------------------------------------------------------
    -- UK National Insurance number: 2 letters, 6 digits, 1 suffix letter (e.g.
    -- SY598539D) = 9 chars; VARCHAR(13) leaves room for the spaced presentation.
    national_insurance_number VARCHAR(13),
    -- The user's PERSONAL 10-digit HMRC Unique Tax Reference (Self Assessment /
    -- MTD for Income Tax). DISTINCT from organisations.utr (the company's UTR).
    utr                       VARCHAR(10),
    -- Date of birth — needed for payroll (age-based NI categories, pension
    -- auto-enrolment thresholds). DATE, not TIMESTAMPTZ: no time/zone component.
    date_of_birth             DATE,

    -- -------------------------------------------------------------------------
    -- Personal / home address
    -- The user's own address (distinct from the organisation's), captured on the
    -- User Details screen for the future payroll module. All nullable + free text;
    -- four flat lines + postcode (mirrors the FreeAgent User Details form rather
    -- than the org's town/region split).
    -- -------------------------------------------------------------------------
    address_line_1            VARCHAR(255),
    address_line_2            VARCHAR(255),
    address_line_3            VARCHAR(255),
    address_line_4            VARCHAR(255),
    postcode                  VARCHAR(20),

    -- -------------------------------------------------------------------------
    -- Email verification
    -- A user must verify their email before they can use the platform.
    -- The verification token is a cryptographically random string emailed to
    -- them on registration. On click, we set email_verified_at = now() and
    -- clear the token.
    -- -------------------------------------------------------------------------
    email_verified_at       TIMESTAMPTZ,                -- NULL = not yet verified
    email_verification_token VARCHAR(100),              -- random token sent in the verification email
    email_verification_sent_at TIMESTAMPTZ,             -- when we last sent the verification email

    -- -------------------------------------------------------------------------
    -- Password reset
    -- Similar pattern to email verification. The token expires after 1 hour
    -- (enforced in the application layer, not the DB).
    -- -------------------------------------------------------------------------
    password_reset_token    VARCHAR(100),
    password_reset_sent_at  TIMESTAMPTZ,

    -- -------------------------------------------------------------------------
    -- Security
    -- -------------------------------------------------------------------------
    -- Track failed login attempts to implement rate limiting / lockout.
    -- Reset to 0 on successful login.
    failed_login_count      INTEGER     NOT NULL DEFAULT 0,
    locked_until            TIMESTAMPTZ,                -- NULL = not locked; set on too many failures
    last_login_at           TIMESTAMPTZ,                -- when the user last successfully authenticated
    last_login_ip           INET,                       -- IP address of last login (INET is a native PG type)

    -- -------------------------------------------------------------------------
    -- Platform administration
    -- -------------------------------------------------------------------------
    -- Platform-wide superuser. Grants READ-ONLY access to the cross-tenant admin
    -- dashboard (browse all organisations + users). This is the ONE privilege
    -- that deliberately breaks org-scoping, so it is set MANUALLY via SQL — like
    -- provider_credentials there is no self-service endpoint, so a normal API
    -- caller can never grant it (not even to themselves). Defaults FALSE, so every
    -- existing account is unaffected and the platform stays fully org-scoped for
    -- everyone who isn't explicitly flagged.
    is_superuser            BOOLEAN     NOT NULL DEFAULT FALSE,

    -- -------------------------------------------------------------------------
    -- Lifecycle
    -- -------------------------------------------------------------------------
    is_active               BOOLEAN     NOT NULL DEFAULT TRUE,
    created_at              TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at              TIMESTAMPTZ NOT NULL DEFAULT now(),
    deleted_at              TIMESTAMPTZ                         -- soft delete
);

-- Index: login lookup by email is the most frequent query on this table
CREATE INDEX idx_users_email ON users (email);

-- Index: verification token lookup — used during email confirmation flow
CREATE INDEX idx_users_verification_token ON users (email_verification_token)
    WHERE email_verification_token IS NOT NULL;

-- Index: password reset token lookup
CREATE INDEX idx_users_reset_token ON users (password_reset_token)
    WHERE password_reset_token IS NOT NULL;

-- Comments
COMMENT ON TABLE users IS 'Platform-level identity. One row per person. A user belongs to one or more organisations via organisation_memberships.';
COMMENT ON COLUMN users.email IS 'Login identifier. Stored lowercase. Globally unique across all tenants.';
COMMENT ON COLUMN users.password_hash IS 'bcrypt hash at cost 12. NULL for OAuth-only accounts. Never store plaintext.';
COMMENT ON COLUMN users.last_login_ip IS 'Uses PostgreSQL native INET type — stores IPv4 and IPv6 without needing a string.';
COMMENT ON COLUMN users.national_insurance_number IS 'UK NINO (payroll). Nullable; format validated in the service layer, not by a CHECK.';
COMMENT ON COLUMN users.utr IS 'User''s PERSONAL 10-digit HMRC Unique Tax Reference (payroll/Self Assessment). Distinct from organisations.utr (the company UTR).';
COMMENT ON COLUMN users.date_of_birth IS 'Date of birth (payroll: NI category by age, pension auto-enrolment). DATE — no time component.';
COMMENT ON COLUMN users.address_line_1 IS 'Personal/home address (payroll). Free text, nullable. Distinct from the organisation address.';
COMMENT ON COLUMN users.postcode IS 'Personal/home postcode (payroll). Free text, nullable.';


-- =============================================================================
-- 3. ORGANISATION_MEMBERSHIPS
-- The join table between users and organisations.
-- This is where the role lives — not on users, because the same person
-- can have different roles at different organisations.
--
-- Example:
--   Alice is 'owner' at Acme Ltd but 'member' at Beta Corp.
--   One user row, two membership rows with different roles.
-- =============================================================================

-- Role enum: define the valid values as a PostgreSQL enum type.
-- Using a type rather than a plain VARCHAR + CHECK constraint gives you:
--   - Cleaner Go type generation via sqlc (a string constant, not a raw string)
--   - Better query plan statistics
--   - Self-documenting schema
--
-- Roles explained:
--   owner       — full control; can delete the organisation and manage billing
--                 only one owner per organisation is enforced at the app layer
--   admin       — can manage users, settings, and all financial data
--                 cannot manage billing or delete the organisation
--   member      — standard user; can create and edit their own expenses/invoices
--                 cannot approve expenses or access admin settings
--   accountant  — read-only access to all financial data; can submit VAT returns
--                 typically an external accountant given limited access
--   read_only   — can view all data but cannot create or edit anything
--                 useful for investors, board observers, or auditors
CREATE TYPE organisation_role AS ENUM (
    'owner',
    'admin',
    'member',
    'accountant',
    'read_only'
);

CREATE TABLE organisation_memberships (
    -- -------------------------------------------------------------------------
    -- Identity
    -- -------------------------------------------------------------------------
    id              UUID        PRIMARY KEY DEFAULT gen_random_uuid(),

    -- Both FKs are NOT NULL — a membership row must reference real rows.
    -- ON DELETE CASCADE: if the organisation or user is hard-deleted (which
    -- should never happen — use soft deletes), the membership is cleaned up.
    -- In practice, soft-deleting either side will hide the membership via
    -- the is_active flag on organisations/users.
    organisation_id UUID        NOT NULL REFERENCES organisations(id) ON DELETE CASCADE,
    user_id         UUID        NOT NULL REFERENCES users(id) ON DELETE CASCADE,

    -- -------------------------------------------------------------------------
    -- Role
    -- -------------------------------------------------------------------------
    role            organisation_role NOT NULL DEFAULT 'member',

    -- -------------------------------------------------------------------------
    -- Invitation flow
    -- When an admin invites someone by email:
    --   1. A membership row is created with status = 'invited'
    --   2. An invitation email is sent with the invite_token
    --   3. The invitee clicks the link, creates an account (or logs in),
    --      and the status is updated to 'active', token cleared
    --
    -- invited_by_user_id: the admin who sent the invite (for audit purposes)
    -- -------------------------------------------------------------------------
    status          VARCHAR(20) NOT NULL DEFAULT 'active'
                    CHECK (status IN ('active','invited','suspended','deactivated')),
    invite_token    VARCHAR(100),                       -- random token sent in invite email; NULL once accepted
    invite_sent_at  TIMESTAMPTZ,
    invite_accepted_at TIMESTAMPTZ,
    invited_by_user_id UUID REFERENCES users(id),      -- NULL for the founding owner (no one invited them)

    -- -------------------------------------------------------------------------
    -- Receipt inbox (email-to-expense)
    -- Each (user, organisation) pair gets one human-readable email address that
    -- receipts can be forwarded to. We store only the LOCAL PART here (e.g.
    -- 'aydin.gunal.acme-ltd'); the full address is local_part || '@' || the
    -- configured INBOX_DOMAIN, so the domain can change without a data migration.
    -- UNIQUE makes the address globally unique, so an inbound email routes with a
    -- single-column lookup. It is NULL until provisioned (generated lazily the
    -- first time the user views it); Postgres treats NULLs as distinct under
    -- UNIQUE, so many un-provisioned rows coexist. Deactivating the membership
    -- (status <> 'active') stops the address resolving — see GetMembershipByInboxLocalPart.
    -- -------------------------------------------------------------------------
    inbox_local_part              VARCHAR(255) UNIQUE,
    inbox_local_part_generated_at TIMESTAMPTZ,

    -- -------------------------------------------------------------------------
    -- Audit
    -- -------------------------------------------------------------------------
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now()

    -- NOTE: no deleted_at here — use status = 'deactivated' instead.
    -- We don't soft-delete memberships; we deactivate them.
    -- This preserves the audit trail of who was a member without orphaning
    -- historical expenses that reference their user_id.
);

-- Unique constraint: one membership per user per organisation.
-- A user cannot be added twice to the same org.
-- This is a UNIQUE constraint on the pair (organisation_id, user_id).
ALTER TABLE organisation_memberships
ADD CONSTRAINT uq_membership UNIQUE (organisation_id, user_id);

-- Indexes
CREATE INDEX idx_memberships_org  ON organisation_memberships (organisation_id) WHERE status = 'active';
CREATE INDEX idx_memberships_user ON organisation_memberships (user_id);
CREATE INDEX idx_memberships_invite_token ON organisation_memberships (invite_token)
    WHERE invite_token IS NOT NULL;


-- =============================================================================
-- 4. EMPLOYEE_PAYROLL
-- The payroll employee-information for a person AT a given organisation (the
-- FreeAgent "Employment / Tax & NI / Monthly Pay / Deductions / Pension" form).
--
-- Why a separate table keyed by (organisation_id, user_id) rather than columns on
-- users: payroll is EMPLOYMENT data, which is org-specific — the same person can be
-- on payroll at more than one organisation with different pay, tax code and NI
-- category. So it parallels organisation_memberships (one row per membership) and is
-- one-to-one with it.
--
-- It is OPTIONAL/derived: a member with no row yet is read as defaults by the API, so
-- the form always renders. Visible/editable to OWNER/ADMIN only (enforced in the
-- members service) — a member cannot see even their own payroll.
--
-- Money is stored as BIGINT minor units (pence), like every other monetary column,
-- and converted to/from decimal pound strings at the API boundary (money package).
-- =============================================================================

CREATE TABLE employee_payroll (
    organisation_id UUID NOT NULL REFERENCES organisations(id) ON DELETE CASCADE,
    user_id         UUID NOT NULL REFERENCES users(id)         ON DELETE CASCADE,

    -- ------------------------------------------------------------------------
    -- Employment details
    -- ------------------------------------------------------------------------
    -- "Is this employee already on your payroll?" — TRUE = existing employee for the
    -- business, FALSE = new employee (the screenshot default).
    is_existing_employee  BOOLEAN NOT NULL DEFAULT FALSE,
    start_date            DATE,                       -- employment start date
    -- HMRC starter declaration A/B/C (see the Starter Checklist).
    starting_declaration  VARCHAR(1) CHECK (starting_declaration IN ('A','B','C')),
    -- How NICs are calculated. Directors are annual; employees monthly.
    nic_calculation       VARCHAR(20) NOT NULL DEFAULT 'employee'
        CHECK (nic_calculation IN ('director','director_alternative','employee')),
    -- Banded normal working hours (drives some HMRC reporting).
    normal_working_hours  VARCHAR(12)
        CHECK (normal_working_hours IN ('under_16','16_to_24','24_to_30','30_plus','other')),
    paid_hourly           BOOLEAN NOT NULL DEFAULT FALSE,   -- payslip varies by hours worked
    paid_irregularly      BOOLEAN NOT NULL DEFAULT FALSE,   -- casual/seasonal/on leave etc.
    payroll_id            VARCHAR(35),                       -- optional HMRC payroll ID

    -- ------------------------------------------------------------------------
    -- Tax and National Insurance
    -- ------------------------------------------------------------------------
    tax_code              VARCHAR(10),                       -- e.g. '1257L', '2207L'
    -- "Make deductions on a Week 1/Month 1 basis?" (non-cumulative tax).
    week1_month1_basis    BOOLEAN NOT NULL DEFAULT FALSE,
    -- HMRC National Insurance category letter (NIC table letter).
    ni_category_letter    VARCHAR(2) NOT NULL DEFAULT 'A'
        CHECK (ni_category_letter IN ('A','B','C','F','H','I','J','L','M','N','S','V','X','Z')),
    student_loan_undergraduate BOOLEAN NOT NULL DEFAULT FALSE,
    student_loan_postgraduate  BOOLEAN NOT NULL DEFAULT FALSE,

    -- ------------------------------------------------------------------------
    -- Monthly Pay — BIGINT pence. Never float for money.
    -- ------------------------------------------------------------------------
    basic_pay_minor                  BIGINT NOT NULL DEFAULT 0,
    allowance_minor                  BIGINT NOT NULL DEFAULT 0,
    other_payments_minor             BIGINT NOT NULL DEFAULT 0,
    pay_not_subject_to_tax_ni_minor  BIGINT NOT NULL DEFAULT 0,

    -- ------------------------------------------------------------------------
    -- Statutory Pay — top-level flag only; the amount detail is deferred (the UI
    -- disables the "Yes" path for now), so this stays FALSE until that ships.
    -- ------------------------------------------------------------------------
    receiving_statutory_pay BOOLEAN NOT NULL DEFAULT FALSE,

    -- ------------------------------------------------------------------------
    -- Monthly Deductions — BIGINT pence.
    -- ------------------------------------------------------------------------
    payroll_giving_minor             BIGINT NOT NULL DEFAULT 0,
    other_deductions_net_pay_minor   BIGINT NOT NULL DEFAULT 0,
    items_class1_nic_not_paye_minor  BIGINT NOT NULL DEFAULT 0,   -- Class 1 NIC-able, not PAYE-taxed
    salary_sacrifice_deductions_minor BIGINT NOT NULL DEFAULT 0,

    -- ------------------------------------------------------------------------
    -- Pension — status only for now; the contribution amounts/scheme are deferred
    -- (the UI disables "Yes, making contributions").
    -- ------------------------------------------------------------------------
    pension_status        VARCHAR(24) NOT NULL DEFAULT 'opted_out_or_ineligible'
        CHECK (pension_status IN ('not_yet_eligible','opted_out_or_ineligible','making_contributions')),

    -- ------------------------------------------------------------------------
    -- Leaving details
    -- ------------------------------------------------------------------------
    leaving_next_pay_run  BOOLEAN NOT NULL DEFAULT FALSE,
    leaving_date          DATE,                        -- set when leaving = Yes

    created_at            TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at            TIMESTAMPTZ NOT NULL DEFAULT now(),

    -- One payroll record per membership.
    PRIMARY KEY (organisation_id, user_id)
);

COMMENT ON TABLE employee_payroll IS 'Per-(organisation,user) payroll employee information (FreeAgent-style). Owner/admin only; one row per membership; optional (absent = defaults). Money in pence.';
COMMENT ON COLUMN employee_payroll.ni_category_letter IS 'HMRC National Insurance category letter (NIC table letter).';
COMMENT ON COLUMN employee_payroll.basic_pay_minor IS 'Monthly basic pay in pence (minor units). Never float.';
COMMENT ON COLUMN employee_payroll.receiving_statutory_pay IS 'Top-level flag; statutory amount detail is deferred (UI disables the Yes path).';
COMMENT ON COLUMN employee_payroll.pension_status IS 'Auto-enrolment status; the making-contributions amount detail is deferred (UI disables that option).';


-- =============================================================================
-- TRIGGERS — auto-update updated_at
-- Reuses the set_updated_at() function already defined in the expenses schema.
-- If you run this file in isolation (e.g. in tests), you need that function
-- to exist first. In production, schemas are applied in order:
--   1. schema.sql (defines set_updated_at)
--   2. auth_schema.sql (this file, reuses it)
-- =============================================================================

CREATE TRIGGER trg_organisations_updated_at
    BEFORE UPDATE ON organisations
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

CREATE TRIGGER trg_users_updated_at
    BEFORE UPDATE ON users
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

CREATE TRIGGER trg_memberships_updated_at
    BEFORE UPDATE ON organisation_memberships
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

CREATE TRIGGER trg_employee_payroll_updated_at
    BEFORE UPDATE ON employee_payroll
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();


-- =============================================================================
-- SEED: Development stub organisation and owner user
--
-- This replaces the hardcoded '00000000-0000-0000-0000-000000000001' UUID
-- that was stubbed into handlers and tests. The same UUIDs are kept
-- intentionally so existing tests don't break.
--
-- Password hash below is bcrypt cost 12 of: 'devpassword123'
-- NEVER use this in production. It's only for local development.
-- Generate a fresh hash with: htpasswd -bnBC 12 "" yourpassword | tr -d ':\n'
-- =============================================================================

INSERT INTO organisations (id, name, slug, native_currency, country_code, timezone, plan, is_active)
VALUES (
    '00000000-0000-0000-0000-000000000001',
    'Development Test Company',
    'dev-test-company',
    'GBP',
    'GB',              -- ISO 3166-1 alpha-2; dev company is UK-based
    'Europe/London',
    'trial',
    TRUE
)
ON CONFLICT (id) DO NOTHING;   -- safe to re-run the seed

INSERT INTO users (id, email, password_hash, first_name, last_name, email_verified_at, is_active)
VALUES (
    '00000000-0000-0000-0000-000000000002',
    'dev@example.com',
    -- bcrypt hash of 'devpassword123' at cost 12
    '$2a$12$tKnyY/tBMSf0.NiyGZRxFeblsneOt2LgqlLQSNgPLdbQF7cKHVaEW',
    'Dev',
    'User',
    now(),                     -- mark as already verified so login works immediately
    TRUE
)
ON CONFLICT (id) DO NOTHING;

INSERT INTO organisation_memberships (organisation_id, user_id, role, status)
VALUES (
    '00000000-0000-0000-0000-000000000001',
    '00000000-0000-0000-0000-000000000002',
    'owner',
    'active'
)
ON CONFLICT DO NOTHING;


ALTER TABLE expenses
ADD CONSTRAINT fk_expenses_organisation
    FOREIGN KEY (organisation_id) REFERENCES organisations(id);

ALTER TABLE expenses
ADD CONSTRAINT fk_expenses_user
    FOREIGN KEY (user_id) REFERENCES users(id);

ALTER TABLE expenses
ADD CONSTRAINT fk_expenses_created_by
    FOREIGN KEY (created_by_user_id) REFERENCES users(id);