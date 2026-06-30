-- =============================================================================
-- BANKING MODULE — DATABASE SCHEMA
-- Accounting SaaS Platform (UK-focused, HMRC MTD-ready)
-- PostgreSQL 15+
--
-- Models the FreeAgent-style "New Bank Account" screen plus the bank-account
-- and transaction list views, and the explain/reconcile workflow. Three tables:
--   - bank_accounts                 — an organisation's own bank accounts (the parent)
--   - bank_transactions             — the lines on each account's statement (the child)
--   - bank_transaction_explanations — the accounting treatment(s) explaining a line
--
-- This is its own bounded context (like auth, contacts, projects): own schema
-- file, own sqlc package (db/banking). Applied AFTER schema.sql + auth_schema.sql +
-- categories_schema.sql + invoices_schema.sql + bills_schema.sql, so set_updated_at(),
-- the organisations/users tables, the categories/transaction_types AND the invoices and
-- bills tables that bank_transaction_explanations references (paid_invoice_id / paid_bill_id)
-- already exist — the foreign keys below are declared INLINE. (invoices + bills pull in
-- contacts_schema.sql + projects_schema.sql, so those are loaded too.)
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
-- The statement lines on an account. Transactions are STORED (manual entry, future
-- feed/statement import) and then EXPLAINED: the explain/reconcile workflow lives in
-- bank_transaction_explanations (below), which links a line to a category, invoice,
-- bill, user or transfer. The `status` column (unexplained / explained / for_approval)
-- is kept in sync from those explanations by the recompute trigger (below); `source`
-- is a plain feed/manual/statement origin tag.
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
    -- Reconcile-tracking (FreeAgent's `unexplained_amount`): the still-unexplained portion of
    -- amount_minor (same SIGNED convention). NULL ⇒ fully unexplained (equals amount_minor);
    -- the future explain/reconcile flow writes the explicit decremented value. The read path
    -- COALESCEs to amount_minor, so existing/seed/feed rows need no backfill.
    unexplained_amount_minor  BIGINT,

    -- Classification (matches the list tabs). `status` is DERIVED from the line's
    -- explanations by the recompute trigger (below); `source` is a plain feed/manual/
    -- statement origin tag.
    status              VARCHAR(20) NOT NULL DEFAULT 'unexplained'
                        CHECK (status IN ('unexplained','explained','for_approval')),
    source              VARCHAR(20) NOT NULL DEFAULT 'manual'
                        CHECK (source IN ('feed','manual','statement')),
    -- OFX/bank transaction type code (FreeAgent's `transaction_type`). Populated by feeds +
    -- statement import; manual entry defaults it by sign (CREDIT in / DEBIT out). Nullable.
    transaction_type    VARCHAR(20)
                        CHECK (transaction_type IN ('CREDIT','DEBIT','INT','DIV','FEE','SRVCHG',
                            'DEP','ATM','POS','XFER','CHECK','PAYMENT','CASH','DIRECTDEP',
                            'DIRECTDEBIT','REPEATPMT','OTHER')),

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


-- -----------------------------------------------------------------------------
-- bank_transaction_explanations
-- "Explaining" a bank transaction: one accounting treatment for (part of) a line.
-- A transaction can have MANY explanations (splitting) — Σ of a transaction's live
-- explanations' gross_value_minor = its amount_minor when fully explained, and the
-- recompute trigger (below) keeps bank_transactions.unexplained_amount_minor +
-- status in sync. Imitates FreeAgent's bank_transaction_explanation; the chosen
-- `type` (transaction_types) decides whether the row carries a category or an
-- entity link. REFERENCE + RECORD only — no double-entry journal lines yet.
-- -----------------------------------------------------------------------------
CREATE TABLE bank_transaction_explanations (
    -- Identity & tenancy
    id                  UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    organisation_id     UUID NOT NULL REFERENCES organisations(id),
    bank_transaction_id UUID NOT NULL REFERENCES bank_transactions(id),  -- the line being explained
    created_by_user_id  UUID REFERENCES users(id),                       -- who explained it (NULL = system guess)

    dated_on            DATE NOT NULL,                                    -- usually the transaction's date
    description         VARCHAR(500),
    -- The explanation type (the FreeAgent "Type" dropdown). FK to the global
    -- transaction_types reference; drives category-vs-entity-link.
    type                VARCHAR(40) NOT NULL REFERENCES transaction_types(code),
    -- This explanation's portion of the transaction, SIGNED like amount_minor
    -- (+ in / - out). Splitting: the portions sum to amount_minor when fully explained.
    gross_value_minor   BIGINT NOT NULL,
    -- The CoA account this posts to. NULL for entity-link types (Transfer / Invoice
    -- Receipt / Bill Payment / …), whose account comes from the linked entity.
    category_id         UUID REFERENCES categories(id),

    -- VAT (reuses the global vat_rates). sales_tax_value_minor is in home-currency pence.
    sales_tax_status    VARCHAR(20) NOT NULL DEFAULT 'TAXABLE'
                        CHECK (sales_tax_status IN ('TAXABLE','EXEMPT','OUT_OF_SCOPE')),
    sales_tax_rate_id   UUID REFERENCES vat_rates(id),
    sales_tax_value_minor BIGINT NOT NULL DEFAULT 0,
    is_manual_sales_tax BOOLEAN NOT NULL DEFAULT FALSE,                  -- TRUE = user typed the VAT (not extracted)
    ec_status           VARCHAR(30),                                     -- UK/Non-EC, Reverse Charge, EC VAT MOSS, …
    place_of_supply     CHAR(2),                                         -- for EC VAT MOSS

    -- Foreign-currency settlement (realised FX). gross_value_minor stays in the BANK
    -- account's currency; these capture the home + invoice-currency views of the same
    -- portion so an invoice receipt is currency-coherent and can crystallise realised
    -- FX gain/loss. All NULL on a home-currency receipt (currency == native).
    --   currency              the bank account's currency at explain time (ISO 4217)
    --   exchange_rate         home (native) units per 1 unit of `currency` on dated_on; NULL when home
    --   base_value_minor      gross_value_minor expressed in HOME currency at the receipt-date rate
    --   settled_invoice_minor the receipt portion expressed in the INVOICE's currency (== gross when same)
    currency              CHAR(3) REFERENCES currencies(code),
    exchange_rate         NUMERIC(18,6),
    base_value_minor      BIGINT,
    settled_invoice_minor BIGINT,

    -- Type-specific links to the settled entities (real FKs): transfer account, user,
    -- invoice and bill below. Links to not-yet-built entities (credit note / HP /
    -- capital asset) are recorded by `type` for now; their FK columns land with that module.
    transfer_bank_account_id UUID REFERENCES bank_accounts(id),          -- Transfer to/from Another Account
    paid_user_id        UUID REFERENCES users(id),                       -- Money Paid/Received to/from User
    -- Invoice Receipt: the sales invoice this receipt settles. NULL for every other
    -- type. The explain service keeps invoices.paid_value_minor = Σ(live INVOICE_RECEIPT
    -- explanations for the invoice) in the same transaction as the explanation write.
    paid_invoice_id     UUID REFERENCES invoices(id),                    -- Invoice Receipt → invoices(id)
    -- Bill Payment: the accounts-payable bill this payment settles. NULL for every
    -- other type. The explain service keeps bills.paid_value_minor = Σ(live BILL_PAYMENT
    -- explanations for the bill) in the same transaction as the explanation write.
    paid_bill_id        UUID REFERENCES bills(id),                       -- Bill Payment → bills(id)

    -- Misc (FreeAgent). marked_for_review = a guessed explanation pending a human OK.
    marked_for_review   BOOLEAN NOT NULL DEFAULT FALSE,
    cheque_number       VARCHAR(50),
    receipt_reference   VARCHAR(100),

    -- Lifecycle & audit (soft delete; the trigger treats deleted rows as gone)
    deleted_at          TIMESTAMPTZ,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Backs ListExplanationsForTransaction + the recompute SUM; org-scoped, live rows.
CREATE INDEX idx_explanations_txn
    ON bank_transaction_explanations (organisation_id, bank_transaction_id) WHERE deleted_at IS NULL;


-- =============================================================================
-- TRIGGERS — auto-update updated_at
-- Reuses the set_updated_at() function defined in db/schema/schema.sql, exactly
-- like auth_schema.sql / contacts_schema.sql do. Schemas are applied in order:
--   1. schema.sql            (defines set_updated_at)
--   2. auth_schema.sql       (organisations, users)
--   3. contacts_schema.sql   (contacts — needed by invoices_schema.sql)
--   4. categories_schema.sql (categories, transaction_types — referenced below)
--   5. invoices_schema.sql   (invoices — referenced by paid_invoice_id below)
--   6. projects_schema.sql   (projects — needed by bills_schema.sql)
--   7. bills_schema.sql      (bills — referenced by paid_bill_id below)
--   8. banking_schema.sql    (this file)
-- =============================================================================
CREATE TRIGGER trg_bank_accounts_updated_at
    BEFORE UPDATE ON bank_accounts
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

CREATE TRIGGER trg_bank_transactions_updated_at
    BEFORE UPDATE ON bank_transactions
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

CREATE TRIGGER trg_explanations_updated_at
    BEFORE UPDATE ON bank_transaction_explanations
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();


-- =============================================================================
-- TRIGGER — recompute a transaction's unexplained amount + status
-- The reconcile mechanic. Whenever a transaction's explanations change (insert,
-- edit, soft-delete/restore, hard-delete), recompute the PARENT bank_transaction:
--   unexplained_amount_minor = amount_minor − Σ(live explanations' gross_value_minor)
--   status = unexplained ≠ 0 → 'unexplained'
--            else any live explanation marked_for_review → 'for_approval'
--            else                                        → 'explained'
-- An AFTER row trigger that RETURNs NULL (mirrors learn_supplier_category() in
-- schema.sql). DB-side so the derived state stays correct whatever writes the
-- explanations. (Explanations are never re-parented to a different transaction,
-- so handling COALESCE(NEW, OLD)'s single parent is sufficient.)
-- =============================================================================
CREATE OR REPLACE FUNCTION recompute_bank_transaction_explained_state()
RETURNS TRIGGER AS $$
DECLARE
    txn_id     UUID;
    txn_amount BIGINT;
    explained  BIGINT;
    has_review BOOLEAN;
BEGIN
    -- The parent is on NEW (INSERT/UPDATE) or OLD (DELETE).
    txn_id := COALESCE(NEW.bank_transaction_id, OLD.bank_transaction_id);

    SELECT amount_minor INTO txn_amount FROM bank_transactions WHERE id = txn_id;

    -- Σ the live explanations' portions + whether any is a guess pending review.
    SELECT COALESCE(SUM(gross_value_minor), 0), COALESCE(bool_or(marked_for_review), FALSE)
        INTO explained, has_review
        FROM bank_transaction_explanations
        WHERE bank_transaction_id = txn_id AND deleted_at IS NULL;

    UPDATE bank_transactions SET
        unexplained_amount_minor = txn_amount - explained,
        status = CASE
                     WHEN txn_amount - explained <> 0 THEN 'unexplained'
                     WHEN has_review                  THEN 'for_approval'
                     ELSE 'explained'
                 END
        WHERE id = txn_id;

    RETURN NULL;  -- AFTER trigger: the return value is ignored
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER trg_explanations_recompute
    AFTER INSERT OR UPDATE OR DELETE ON bank_transaction_explanations
    FOR EACH ROW EXECUTE FUNCTION recompute_bank_transaction_explained_state();


-- =============================================================================
-- COMMENTS
-- =============================================================================
COMMENT ON TABLE  bank_accounts IS 'An organisation''s own bank accounts. Org-scoped, soft-deleted. Models the FreeAgent New Bank Account screen. Current balance is derived (opening + SUM transactions), never stored.';
COMMENT ON COLUMN bank_accounts.opening_balance_minor IS 'Opening balance in minor units (pence). BIGINT/int64: bank balances accumulate past the int32 single-expense ceiling. Never float.';
COMMENT ON COLUMN bank_accounts.is_primary IS 'The org''s primary account. At most one live primary per org, enforced by idx_bank_accounts_one_primary.';
COMMENT ON COLUMN bank_accounts.sort_code IS 'UK branch code (6 digits). US accounts use routing_number instead; international use iban/bic.';
COMMENT ON COLUMN bank_accounts.routing_number IS 'US ABA routing number (9 digits). NULL for non-US accounts.';
COMMENT ON TABLE  bank_transactions IS 'Statement lines on a bank account. amount_minor is signed (+ in / - out). Org-scoped + soft-deleted. Explained via bank_transaction_explanations; status (unexplained/explained/for_approval) is kept in sync by the recompute trigger.';
COMMENT ON COLUMN bank_transactions.amount_minor IS 'Signed minor units (pence): POSITIVE = money in, NEGATIVE = money out. BIGINT/int64.';
COMMENT ON COLUMN bank_transactions.external_id IS 'Bank feed provider transaction id, for dedupe. NULL for manual rows; unique per account when present (idx_bank_transactions_external).';
COMMENT ON TABLE  bank_transaction_explanations IS 'Accounting explanations for bank transactions (the reconcile record). Many per transaction (splitting). gross_value_minor is signed (+ in / - out). type drives category-vs-entity-link. The recompute trigger keeps bank_transactions.unexplained_amount_minor + status in sync. No double-entry journal yet.';
COMMENT ON COLUMN bank_transaction_explanations.gross_value_minor IS 'This explanation''s portion of the transaction, signed minor units (pence). Σ of a transaction''s live explanations = its amount_minor when fully explained.';
COMMENT ON COLUMN bank_transaction_explanations.category_id IS 'CoA account (categories) this posts to. NULL for entity-link types (Transfer / Invoice Receipt / Bill Payment / …), whose account comes from the linked entity.';
COMMENT ON COLUMN bank_transaction_explanations.marked_for_review IS 'TRUE = a guessed/auto explanation pending human approval; drives the parent transaction''s status = for_approval (see recompute trigger).';
COMMENT ON COLUMN bank_transaction_explanations.paid_invoice_id IS 'Invoice Receipt only: the sales invoice this receipt settles (NULL for every other type). The explain service keeps invoices.paid_value_minor = Σ(live INVOICE_RECEIPT explanations for the invoice) in the same transaction as the explanation write.';
COMMENT ON COLUMN bank_transaction_explanations.paid_bill_id IS 'Bill Payment only: the accounts-payable bill this payment settles (NULL for every other type). The explain service keeps bills.paid_value_minor = Σ(live BILL_PAYMENT explanations for the bill) in the same transaction as the explanation write.';
