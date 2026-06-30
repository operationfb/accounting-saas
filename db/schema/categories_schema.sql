-- =============================================================================
-- CHART OF ACCOUNTS + RECONCILE REFERENCE — DATABASE SCHEMA
-- Accounting SaaS Platform (UK-focused, HMRC MTD-ready)
-- PostgreSQL 15+
--
-- The reference data behind "explaining" a bank transaction (the reconcile epic).
-- Modelled on FreeAgent's Chart of Accounts + bank-transaction explanation types.
-- Three tables:
--   - categories                  — the Chart of Accounts (per-org), the nominal
--                                    accounts an explanation can post to.
--   - transaction_types           — the 18 explanation types (GLOBAL reference),
--                                    e.g. Payment, Sales, Transfer (the screenshot).
--   - transaction_type_categories — the MAPPING (GLOBAL): which categories each
--                                    type offers, i.e. "every category per type"
--                                    AND which nominal account that pair posts to.
--
-- This increment is REFERENCE DATA + the explanation record (see banking_schema.sql)
-- only — there is NO double-entry ledger/posting engine yet. "Which account is used
-- for a type-category pair" lives in transaction_type_categories as metadata, not as
-- live journal lines. Per-entity sub-accounts (750-x bank, 900-x user, 602-x asset)
-- are represented by entity links on the explanation, not seeded here.
--
-- Applied AFTER schema.sql + auth_schema.sql (uses set_updated_at() + organisations)
-- and BEFORE banking_schema.sql (whose bank_transaction_explanations FKs categories
-- + transaction_types). Application order:
--   1. schema.sql           (set_updated_at, vat_rates)
--   2. auth_schema.sql      (organisations, users)
--   3. categories_schema.sql (this file)
--   4. banking_schema.sql   (bank_accounts/bank_transactions + the explanations)
--
-- Design decisions worth knowing:
--
--   THE SINGLE category table (since 2026-06-30).
--   This is now the ONE Chart of Accounts for the whole app — expenses, bills,
--   banking, invoices, payroll and the GL all post against it. The expenses
--   module's old per-module expense_categories table (a different nominal-code
--   scheme: '365 Travel' vs FreeAgent's '254 Travel & Subsistence') was dropped
--   and folded in here; see the EXPENSES → CHART OF ACCOUNTS WIRING section at
--   the foot of this file (the expenses FK + the v_expenses_full view).
--
--   categories IS PER-ORG; the two reference tables are GLOBAL.
--   Each org gets its own seeded CoA (+ custom additions).
--   transaction_types and the mapping are GLOBAL reference (like vat_rates / currencies)
--   — the 18 types and their offered nominal codes are the same for everyone. The
--   mapping therefore SOFT-LINKS by nominal_code (resolved against the caller's org's
--   categories at query time), since a global table can't FK a per-org one.
-- =============================================================================


-- -----------------------------------------------------------------------------
-- categories
-- The Chart of Accounts: every nominal account an explanation (or, later, an
-- invoice/bill/journal) can post to. Per-organisation, seeded from the FreeAgent
-- workbook, custom additions allowed within FreeAgent's user-creatable ranges.
-- -----------------------------------------------------------------------------
CREATE TABLE categories (
    -- Identity & tenancy
    id                  UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    organisation_id     UUID NOT NULL REFERENCES organisations(id),   -- tenant that owns this CoA row

    nominal_code        VARCHAR(20)  NOT NULL,                        -- FreeAgent nominal: '001','254','602','907'
    name                VARCHAR(150) NOT NULL,                        -- 'Sales', 'Travel and Subsistence', 'Drawings'
    description         TEXT,

    -- The CoA section (the workbook's P&L / Balance-Sheet groupings). Drives report
    -- grouping AND which transaction types may offer the account.
    account_type        VARCHAR(20)  NOT NULL CHECK (account_type IN (
                            'INCOME','OTHER_INCOME','COST_OF_SALES','ADMIN_EXPENSE',
                            'PAYROLL_EXPENSE','CAPITAL_ASSET','CURRENT_ASSET','BANK',
                            'LIABILITY','TAX_LIABILITY','USER_ACCOUNT','EQUITY','SYSTEM')),

    -- FreeAgent's API category group (the JSON key the explain picker filters by).
    -- The broad money-in/out types (Payment, Refund, Sales) offer whole api_groups.
    api_group           VARCHAR(30)  CHECK (api_group IN (
                            'income_categories','cost_of_sales_categories',
                            'admin_expenses_categories','general_categories')),

    tax_reporting_name  VARCHAR(150),                                 -- HMRC P&L reporting name (Ltd), nullable
    allowable_for_tax   BOOLEAN,                                      -- tax-deductible? NULL = n/a (balance sheet)
    -- Default VAT treatment pre-filled when this category is picked on an explanation
    -- (the explain VAT can still be overridden). NULL = unset.
    default_vat         VARCHAR(20)  CHECK (default_vat IN (
                            'STANDARD','REDUCED','ZERO','EXEMPT','OUTSIDE_SCOPE')),

    is_capital_asset    BOOLEAN NOT NULL DEFAULT FALSE,               -- a fixed-asset account (depreciation later)
    -- TRUE = FreeAgent-managed (VAT control, debtors, user sub-accounts, …): posted
    -- to automatically, never offered as a free pick for a normal explanation.
    is_system_managed   BOOLEAN NOT NULL DEFAULT FALSE,

    -- Per-USER sub-accounts (FreeAgent's 908-1, 907-2, … one per director). A PARENT
    -- account flagged is_user_subdivided is NEVER posted to directly when the event
    -- links a user: the general-ledger resolver (internal/ledger.Accounts) expands it
    -- to THAT user's sub-account ROW, creating it lazily. So a Dividend (908) paid to
    -- director A lands in 908-1, to director B in 908-2 — each its own ledger account,
    -- the parent being the roll-up.
    is_user_subdivided  BOOLEAN NOT NULL DEFAULT FALSE,               -- TRUE on a parent that splits per user (all USER_ACCOUNT 900–910)
    -- Set ONLY on a sub-account row: the parent nominal it subdivides and the OWNER it
    -- belongs to — a user (e.g. '908-1', parent '908', user_id = A) OR a bank account
    -- (e.g. '750-1', parent '750', bank_account_id = the account). NULL on every
    -- normal / parent account. Same "sub-account is its own row" shape as the seeded
    -- 602-x capital-asset sub-accounts, extended with an owner link.
    --
    -- bank_account_id is a PLAIN column (no FK): categories is created BEFORE
    -- bank_accounts in the DDL/sqlc order, so an FK would be a cycle. Soft-linked like
    -- transaction_type_categories; integrity is enforced by the ledger resolver, which
    -- is the only writer of these rows.
    parent_nominal_code VARCHAR(20),
    user_id             UUID REFERENCES users(id),
    bank_account_id     UUID,                                        -- the bank account a 750-x sub-account belongs to

    -- A sub-account has at most ONE owner kind.
    CONSTRAINT ck_categories_subaccount_owner CHECK (user_id IS NULL OR bank_account_id IS NULL),

    is_active           BOOLEAN NOT NULL DEFAULT TRUE,

    created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT now(),

    -- One row per (org, nominal) — the natural key + the soft-link target for the
    -- mapping below and the ON CONFLICT key for the idempotent seed.
    CONSTRAINT uq_categories_nominal UNIQUE (organisation_id, nominal_code)
);

-- Backs ListCategories / GetCategory (org-scoped, live rows only).
CREATE INDEX idx_categories_org       ON categories (organisation_id) WHERE is_active;
-- Backs the explain picker filtering a type's offered api_group(s) to the org's CoA.
CREATE INDEX idx_categories_org_group ON categories (organisation_id, api_group) WHERE is_active;

-- At most ONE user sub-account per (org, parent account, user) — the DB guarantee
-- behind the resolver's idempotent find-or-create (two concurrent posts for the same
-- director can't make 908-1 AND 908-2 for them). Partial: only the sub-account rows.
CREATE UNIQUE INDEX idx_categories_user_subaccount
    ON categories (organisation_id, parent_nominal_code, user_id)
    WHERE user_id IS NOT NULL;

-- At most ONE ledger account per (org, bank account) — FreeAgent's 750-x sub-accounts,
-- one per bank account. Same idempotent-find-or-create guarantee as the user index.
CREATE UNIQUE INDEX idx_categories_bank_subaccount
    ON categories (organisation_id, parent_nominal_code, bank_account_id)
    WHERE bank_account_id IS NOT NULL;


-- -----------------------------------------------------------------------------
-- transaction_types  (GLOBAL reference, like vat_rates / currencies)
-- The 18 explanation types from the FreeAgent "Type" dropdown, grouped Money
-- In / Money Out. `entity_link` records what (if anything) the type links to
-- instead of / alongside a category (another bank account, a user, an invoice…).
-- -----------------------------------------------------------------------------
CREATE TABLE transaction_types (
    code            VARCHAR(40) PRIMARY KEY,                          -- 'PAYMENT','SALES','TRANSFER_TO_ACCOUNT',…
    name            VARCHAR(60) NOT NULL,                             -- 'Payment','Sales','Transfer to Another Account'
    direction       VARCHAR(3)  NOT NULL CHECK (direction IN ('in','out')),
    -- The entity (if any) this type links to. NONE = pure category explanation;
    -- the others carry a link on the explanation (some, e.g. INVOICE/BILL, are
    -- valid types now but their dedicated link column lands with that module).
    entity_link     VARCHAR(20) NOT NULL DEFAULT 'NONE' CHECK (entity_link IN (
                        'NONE','BANK_ACCOUNT','USER','INVOICE','BILL',
                        'CREDIT_NOTE','HP_AGREEMENT','CAPITAL_ASSET')),
    display_order   INTEGER NOT NULL DEFAULT 0,                       -- order within its direction group
    is_active       BOOLEAN NOT NULL DEFAULT TRUE,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);


-- -----------------------------------------------------------------------------
-- transaction_type_categories  (GLOBAL mapping)
-- THE model the user asked for: "every category available per transaction type",
-- and — since each row's nominal_code resolves to a CoA account — "which account
-- is used for each type-category pair".
--
-- nominal_code is a SOFT link to categories.nominal_code (no FK: this table is
-- global, categories is per-org), resolved against the caller's org at query time.
-- company_type lets the offered options differ by org type (e.g. Money Paid to
-- User → Ltd: Salary/Dividend/Director's Loan; Sole trader: Drawings). 'ALL' = any.
-- -----------------------------------------------------------------------------
CREATE TABLE transaction_type_categories (
    transaction_type_code VARCHAR(40) NOT NULL REFERENCES transaction_types(code),
    -- A mapping row offers EITHER one specific nominal_code OR a whole api_group.
    -- The broad money-in/out types offer entire groups (e.g. Payment → every admin
    -- expense + cost of sales); the rest offer specific accounts (e.g. Other Money
    -- Out → 813 Pension Creditor). Exactly one is set; the other is '' (empty
    -- sentinel, NOT NULL, so the UNIQUE key + ON CONFLICT seed behave). Both
    -- soft-resolve against the caller's org's categories at query time.
    nominal_code          VARCHAR(20) NOT NULL DEFAULT '',            -- a specific account, or ''
    api_group             VARCHAR(30) NOT NULL DEFAULT '' CHECK (api_group IN (
                              '','income_categories','cost_of_sales_categories',
                              'admin_expenses_categories','general_categories')),
    -- 'ALL' = offered for every company type; else only this one (matches
    -- organisations.company_type). NOT NULL + 'ALL' sentinel (not NULL) so the
    -- UNIQUE key and the idempotent-seed ON CONFLICT behave.
    company_type          VARCHAR(20) NOT NULL DEFAULT 'ALL' CHECK (company_type IN (
                              'ALL','limited','sole_trader','partnership','landlord','corporation')),
    display_label         VARCHAR(100),                               -- override label (e.g. tab's 'BiK' for 907); NULL = category name
    display_order         INTEGER NOT NULL DEFAULT 0,
    created_at            TIMESTAMPTZ NOT NULL DEFAULT now(),

    -- Exactly one of nominal_code / api_group is set (XOR of "is non-empty").
    CONSTRAINT ck_ttc_target CHECK ((nominal_code <> '') <> (api_group <> '')),
    CONSTRAINT uq_ttc UNIQUE (transaction_type_code, nominal_code, api_group, company_type)
);

-- Backs ListCategoriesForType (type [+ company_type] → its offered nominals).
CREATE INDEX idx_ttc_type ON transaction_type_categories (transaction_type_code, company_type);


-- =============================================================================
-- TRIGGERS — auto-update updated_at (reuses set_updated_at() from schema.sql)
-- Only categories is mutable; the two reference tables are static (seeded), like
-- vat_rates, so they carry created_at only and need no updated_at trigger.
-- =============================================================================
CREATE TRIGGER trg_categories_updated_at
    BEFORE UPDATE ON categories
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();


-- =============================================================================
-- COMMENTS
-- =============================================================================
COMMENT ON TABLE  categories IS 'Per-org Chart of Accounts (FreeAgent nominal codes). The single category table for the whole app — expenses, bills, banking, invoices, payroll and the GL all post against it.';
COMMENT ON COLUMN categories.account_type      IS 'CoA section (workbook tab): INCOME, COST_OF_SALES, ADMIN_EXPENSE, CAPITAL_ASSET, USER_ACCOUNT, etc. Drives report grouping + which transaction types offer the account.';
COMMENT ON COLUMN categories.api_group         IS 'FreeAgent API category group (income_categories / cost_of_sales_categories / admin_expenses_categories / general_categories). Broad explanation types offer whole groups.';
COMMENT ON COLUMN categories.is_system_managed IS 'TRUE = FreeAgent-managed control account (VAT, debtors, user sub-accounts). Posted to automatically; not a free pick for explanations.';
COMMENT ON TABLE  transaction_types IS 'The 18 bank-transaction explanation types (GLOBAL reference, like vat_rates). entity_link records what the type links to (bank account, user, invoice…).';
COMMENT ON TABLE  transaction_type_categories IS 'GLOBAL mapping: which CoA accounts each transaction type offers (every category per type), branched by company_type. A row targets EITHER a specific nominal_code OR a whole api_group; both SOFT-link categories (resolved per org).';
COMMENT ON COLUMN transaction_type_categories.company_type IS 'ALL = offered for every company type; else only this organisations.company_type (e.g. Money Paid to User differs Ltd vs sole trader).';


-- =============================================================================
-- EXPENSES → CHART OF ACCOUNTS WIRING (2026-06-30 unification)
-- The expenses module was unified onto this shared CoA: its old per-module
-- expense_categories table was dropped, and expenses.category_id /
-- supplier_category_map.category_id now reference categories(id).
--
-- These FKs (and the v_expenses_full view below) are declared HERE, not in
-- schema.sql, purely for ORDERING: schema.sql is applied first and creates
-- expenses + expense_mileage + supplier_category_map, but `categories` does not
-- exist until this file. Both objects need expenses AND categories to exist, so
-- they live at the join point — the same reason categories.bank_account_id is a
-- plain (soft-linked) column rather than an FK.
-- =============================================================================

ALTER TABLE expenses
    ADD CONSTRAINT fk_expenses_category
    FOREIGN KEY (category_id) REFERENCES categories(id);

ALTER TABLE supplier_category_map
    ADD CONSTRAINT fk_supplier_category_category
    FOREIGN KEY (category_id) REFERENCES categories(id);


-- -----------------------------------------------------------------------------
-- v_expenses_full
-- A convenience view joining expenses with their mileage sub-record and category
-- (the shared CoA). Used by the expenses service's "get expense" / list / detail
-- reads so the JOIN isn't repeated. Plain view (not materialised) — always fresh.
-- Lives here (moved from schema.sql) because it JOINs categories, created above.
-- -----------------------------------------------------------------------------
DROP VIEW IF EXISTS v_expenses_full;
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
    -- Category fields (from the shared Chart of Accounts, categories)
    ec.nominal_code         AS category_nominal_code,
    ec.name                 AS category_name,
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
    e.updated_at,
    -- Raw FKs (the rest of the view exposes names) — used to pre-fill the edit form.
    e.category_id,
    e.vat_rate_id,
    -- Capture / OCR fields. needs_review drives the Smart Upload review inbox;
    -- ocr_confidence/ocr_processed_at let the detail screen flag low-confidence captures.
    e.needs_review,
    e.ocr_confidence,
    e.ocr_processed_at,
    -- Rejection reason for a REJECTED expense.
    e.rejection_note
FROM expenses e
JOIN categories ec ON ec.id = e.category_id            -- the shared Chart of Accounts
LEFT JOIN expense_mileage em ON em.expense_id = e.id   -- LEFT JOIN: only present for mileage claims
WHERE e.deleted_at IS NULL;                             -- soft delete filter always applied
