-- =============================================================================
-- BANKING MODULE — DATABASE SCHEMA
-- Accounting SaaS Platform (UK-focused, HMRC MTD-ready)
-- PostgreSQL 15+
--
-- Models the FreeAgent-style "New Bank Account" screen plus the bank-account
-- and transaction list views. Two tables:
--   - bank_accounts      — an organisation's own bank accounts (the parent)
--   - bank_transactions  — the lines on each account's statement (the child)
--
-- This is its own bounded context (like auth, contacts, projects): own schema
-- file, own sqlc package (db/banking). Applied AFTER schema.sql + auth_schema.sql,
-- so the set_updated_at() trigger function and the organisations/users tables it
-- references already exist — the foreign keys below are declared INLINE.
--
-- Design decisions worth knowing:
--
--   MONEY IS BIGINT (int64), NOT INTEGER.
--   Like the rest of the platform, money is stored as INTEGER MINOR UNITS (pence),
--   never float. But unlike a single expense (INTEGER / int32, ~£21.4m ceiling),
--   a BANK BALANCE accumulates and easily exceeds that for a business, so every
--   amount here is BIGINT — exactly what the money kernel speaks
--   (money.MinorToPounds(int64), money.PoundsToMinor → int64).
--
--   BALANCE IS DERIVED, NEVER STORED.
--   An account's current balance is opening_balance_minor + SUM(its transactions'
--   amount_minor), computed in the List/Get queries. A stored, mutable balance
--   column would drift out of sync with the transactions; deriving it keeps a
--   single source of truth.
--
--   ONE SIGNED AMOUNT PER TRANSACTION.
--   bank_transactions.amount_minor is SIGNED: POSITIVE = money in, NEGATIVE =
--   money out. One column (not separate money_in/money_out) makes SUM() for the
--   balance trivial and matches the signed-minor-unit convention used elsewhere.
--
--   AT MOST ONE PRIMARY ACCOUNT PER ORG.
--   "Make this my primary account" is enforced by a PARTIAL UNIQUE INDEX, so the
--   database — not just the app — guarantees a single primary per organisation.
--
--   DISCRETE PER-COUNTRY BANK CODES.
--   Rather than one overloaded field, each scheme gets its own column: sort_code
--   (UK), routing_number (US ABA), iban/bic (international), bank_account_type
--   (US checking/savings). Explicit over clever — a country fills whichever apply
--   and leaves the rest NULL.
--
--   FEED DEDUPE READY.
--   external_id holds a bank feed's transaction id (TrueLayer/Open Banking, later)
--   so a re-delivered transaction can't double-insert; a partial unique index
--   enforces it. NULL for manually-added rows.
--
--   MULTI-TENANCY + SOFT DELETE.
--   organisation_id scopes every row and leads every index; deleted_at is a soft
--   delete (financial records are never hard-removed). created_at/updated_at are
--   stamped, updated_at by the shared set_updated_at() trigger.
-- =============================================================================


-- -----------------------------------------------------------------------------
-- bank_accounts
-- The org's own bank accounts (current/savings, GBP or foreign). Maps the
-- "New Bank Account" form's fields.
-- -----------------------------------------------------------------------------
CREATE TABLE bank_accounts (
    -- Identity & tenancy
    id                  UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    organisation_id     UUID NOT NULL REFERENCES organisations(id),  -- tenant that OWNS this account
    created_by_user_id  UUID NOT NULL REFERENCES users(id),          -- who created the record (audit)

    -- Bank account (the form's top section)
    name                VARCHAR(200) NOT NULL,                       -- "Account name" (required)
    currency            CHAR(3) NOT NULL DEFAULT 'GBP'               -- ISO 4217, uppercase (matches organisations.native_currency)
                        CHECK (currency = upper(currency)),
    -- Lifecycle status. The form is a dropdown (not a checkbox), so an enum, not a
    -- bool. 'closed' = the account is shut but kept for history. Easily extended.
    status              VARCHAR(20) NOT NULL DEFAULT 'active'
                        CHECK (status IN ('active','closed')),
    is_personal         BOOLEAN NOT NULL DEFAULT FALSE,              -- "This is a personal account"
    is_primary          BOOLEAN NOT NULL DEFAULT FALSE,              -- "Make this my primary account" (≤1 per org, see index)

    -- Optional details + account identifiers (see "DISCRETE PER-COUNTRY BANK CODES")
    bank_name           VARCHAR(200),                                -- "Bank name", e.g. 'NatWest'
    account_number      VARCHAR(34),                                 -- UK 8-digit / US (up to ~17) / other; widened for intl
    sort_code           VARCHAR(8),                                  -- UK branch code, 6 digits ('12-34-56')
    routing_number      VARCHAR(20),                                 -- US ABA routing number, 9 digits
    bank_account_type   VARCHAR(20)                                  -- US ACH needs checking|savings; NULL elsewhere
                        CHECK (bank_account_type IN ('checking','savings')),
    iban                VARCHAR(34),                                 -- international account number (e.g. Revolut EUR)
    bic                 VARCHAR(11),                                 -- SWIFT/BIC, 8 or 11 chars
    show_on_invoices    BOOLEAN NOT NULL DEFAULT TRUE,               -- "Show these details on Invoices" (form default: checked)

    -- Opening balance (at the accounting/FreeAgent start date). The current
    -- balance is NOT stored here — it is derived (see "BALANCE IS DERIVED").
    opening_balance_minor  BIGINT NOT NULL DEFAULT 0,                -- minor units (pence); BIGINT — balances exceed int32
    opening_balance_date   DATE,                                     -- "at start of <date>"; NULL = org accounting start

    guess_explanations  BOOLEAN NOT NULL DEFAULT TRUE,               -- "Guess explanations for my transactions" (form default: checked)

    -- Lifecycle & audit
    deleted_at          TIMESTAMPTZ,                                 -- NULL = active; set to soft-delete
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Backs ListBankAccounts and the per-id org-scoped lookups; carries only live rows.
CREATE INDEX idx_bank_accounts_org ON bank_accounts (organisation_id) WHERE deleted_at IS NULL;

-- At most ONE primary account per organisation. Partial so it ignores closed/
-- soft-deleted rows and only constrains the live ones. The service clears the old
-- primary (UnsetPrimaryBankAccounts) before setting a new one, within a tx.
CREATE UNIQUE INDEX idx_bank_accounts_one_primary
    ON bank_accounts (organisation_id) WHERE is_primary AND deleted_at IS NULL;


-- -----------------------------------------------------------------------------
-- bank_transactions
-- The statement lines on an account. v1 STORES transactions (manual entry, future
-- feed/statement import); the "explain"/reconciliation workflow that links a line
-- to an expense or invoice is deferred (see BACKLOG.md) — hence status/source are
-- plain classification columns for now, with no link-to-expense FK yet.
-- -----------------------------------------------------------------------------
CREATE TABLE bank_transactions (
    -- Identity & tenancy
    id                  UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    -- organisation_id is denormalised onto the line (it is implied by the account)
    -- so EVERY query can be org-scoped without joining bank_accounts.
    organisation_id     UUID NOT NULL REFERENCES organisations(id),
    bank_account_id     UUID NOT NULL REFERENCES bank_accounts(id),  -- the account this line belongs to
    -- Who recorded it. NULL = arrived from a bank feed (no human); set = manually added.
    created_by_user_id  UUID REFERENCES users(id),

    dated_on            DATE NOT NULL,                               -- "Date" the transaction is dated
    -- SIGNED minor units (pence): POSITIVE = money in, NEGATIVE = money out.
    -- e.g. +1641359 = £16,413.59 in; -8648 = £86.48 out. BIGINT like the balance.
    amount_minor        BIGINT NOT NULL,
    description         VARCHAR(500),                                -- human description, e.g. 'Uber Uk Rides'
    bank_memo           TEXT,                                        -- raw bank narrative, e.g. 'Ubr* Pending.uber.com//OTHER/£10.08'
    balance_minor       BIGINT,                                      -- running balance reported on the statement (NULL if unknown)

    -- Classification (matches the list tabs). Reconciliation/linking is deferred,
    -- so these are plain CHECK-constrained columns for now.
    status              VARCHAR(20) NOT NULL DEFAULT 'unexplained'
                        CHECK (status IN ('unexplained','explained','for_approval')),
    source              VARCHAR(20) NOT NULL DEFAULT 'manual'
                        CHECK (source IN ('feed','manual','statement')),

    -- Bank-feed dedupe: the provider's own transaction id (TrueLayer, later).
    -- NULL for manual rows; unique per account when present (see index below).
    external_id         VARCHAR(255),

    -- Lifecycle & audit
    deleted_at          TIMESTAMPTZ,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Backs ListBankTransactions (per account, newest first) and the derived-balance
-- SUM join; org_id leads, carries only live rows.
CREATE INDEX idx_bank_transactions_account
    ON bank_transactions (organisation_id, bank_account_id, dated_on) WHERE deleted_at IS NULL;

-- Feed dedupe: a provider's transaction id can't double-insert into one account.
-- Partial (only when external_id is present and the row is live), so manual rows
-- (external_id NULL) are unconstrained and many can coexist.
CREATE UNIQUE INDEX idx_bank_transactions_external
    ON bank_transactions (bank_account_id, external_id)
    WHERE external_id IS NOT NULL AND deleted_at IS NULL;


-- =============================================================================
-- TRIGGERS — auto-update updated_at
-- Reuses the set_updated_at() function defined in db/schema/schema.sql, exactly
-- like auth_schema.sql / contacts_schema.sql do. Schemas are applied in order:
--   1. schema.sql          (defines set_updated_at)
--   2. auth_schema.sql     (organisations, users)
--   3. banking_schema.sql  (this file)
-- =============================================================================
CREATE TRIGGER trg_bank_accounts_updated_at
    BEFORE UPDATE ON bank_accounts
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

CREATE TRIGGER trg_bank_transactions_updated_at
    BEFORE UPDATE ON bank_transactions
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();


-- =============================================================================
-- COMMENTS
-- =============================================================================
COMMENT ON TABLE  bank_accounts IS 'An organisation''s own bank accounts. Org-scoped, soft-deleted. Models the FreeAgent New Bank Account screen. Current balance is derived (opening + SUM transactions), never stored.';
COMMENT ON COLUMN bank_accounts.opening_balance_minor IS 'Opening balance in minor units (pence). BIGINT/int64: bank balances accumulate past the int32 single-expense ceiling. Never float.';
COMMENT ON COLUMN bank_accounts.is_primary IS 'The org''s primary account. At most one live primary per org, enforced by idx_bank_accounts_one_primary.';
COMMENT ON COLUMN bank_accounts.sort_code IS 'UK branch code (6 digits). US accounts use routing_number instead; international use iban/bic.';
COMMENT ON COLUMN bank_accounts.routing_number IS 'US ABA routing number (9 digits). NULL for non-US accounts.';
COMMENT ON TABLE  bank_transactions IS 'Statement lines on a bank account. amount_minor is signed (+ in / - out). Org-scoped + soft-deleted. Reconciliation/explain is deferred (BACKLOG).';
COMMENT ON COLUMN bank_transactions.amount_minor IS 'Signed minor units (pence): POSITIVE = money in, NEGATIVE = money out. BIGINT/int64.';
COMMENT ON COLUMN bank_transactions.external_id IS 'Bank feed provider transaction id, for dedupe. NULL for manual rows; unique per account when present (idx_bank_transactions_external).';
