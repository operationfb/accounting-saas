-- =============================================================================
-- GENERAL LEDGER MODULE — DATABASE SCHEMA
-- Accounting SaaS Platform (UK-focused, HMRC MTD-ready)
-- PostgreSQL 15+
--
-- The double-entry ledger that sits BEHIND the forefront modules (expenses,
-- invoices, bills, banking). See the design doc for the full picture; this file
-- is ITERATION 1, which adds ONLY the posting-rules reference table. The journal
-- tables (gl_journal_entries + gl_journal_lines) and the Σ=0 balance trigger land
-- in a later iteration alongside the posting engine.
--
--   gl_posting_rules — the double-entry MAPPING, stored as DATA (not Go). For each
--                      economic event it lists the journal legs: which account
--                      (by symbolic ROLE), which money component, and Dr/Cr. A
--                      generic interpreter reads these rows to post entries, so
--                      adding/changing a rule is a seed change, not a code change.
--                      GLOBAL reference data (no organisation_id), exactly like
--                      transaction_types / transaction_type_categories.
--
-- Design decisions worth knowing:
--
--   THE MAPPING IS DATA, NOT CODE.
--   Mirrors transaction_type_categories (the explain picker's mapping) and the
--   FreeAgent-push Cloud Workflow (whose field mapping is externalised YAML). The
--   Dr/Cr recipe for every event is rows here, validated against FreeAgent's own
--   chart of accounts / trial balance (see gl_posting_rules_freeagent_test.go).
--
--   ROLES, NOT HARDCODED NOMINALS.
--   account_role is a SYMBOLIC target (BANK, DEBTORS, VAT_CONTROL, …) resolved to
--   a real categories row at POST time. EXPLANATION_CATEGORY / SOURCE_CATEGORY
--   resolve to the live category the user picked, so one rule is correct for every
--   one of the hundreds of CoA categories — granular without a row per category.
--
--   SIGNED, BALANCE-AS-SUM CONVENTION (for the journal lines this drives).
--   direction is DR/CR; the interpreter posts +amount for DR, −amount for CR, so a
--   posted entry sums to zero and an account balance is SUM(amount_minor) — the
--   same signed-minor convention bank_accounts already uses for its derived balance.
--
--   event_code IS FREE TEXT (no FK to transaction_types).
--   For bank explanations it equals transaction_types.code (PAYMENT, SALES, …); for
--   the non-bank events it is a synthetic code (EXPENSE_APPROVED, INVOICE_SENT,
--   BILL_CREATED, BANK_OPENING) that has no transaction_types row — so no FK.
--
-- Applied AFTER schema.sql + auth_schema.sql (it needs neither today — no FK — but
-- is loaded in dependency order with the other domains).
-- =============================================================================


-- -----------------------------------------------------------------------------
-- gl_posting_rules
-- One row per (event, leg). The legs of an event_code together form one balanced
-- journal entry. GLOBAL reference data, seeded from db/seeds/gl_posting_rules.sql.
-- -----------------------------------------------------------------------------
CREATE TABLE gl_posting_rules (
    -- The economic event. Bank explanations: == transaction_types.code. Non-bank:
    -- EXPENSE_APPROVED / INVOICE_SENT / BILL_CREATED / BANK_OPENING (synthetic).
    event_code           VARCHAR(40) NOT NULL,
    -- Ordinal of this leg within the entry (1, 2, 3 …). Drives display order too.
    leg_no               INTEGER     NOT NULL,

    -- The SYMBOLIC account this leg posts to, resolved to a categories row at post
    -- time by the ledger.Accounts resolver:
    --   BANK                 the bank line's own account (per-account 750-x)
    --   DEBTORS              receivables control (681 Trade Debtors)
    --   CREDITORS            payables control (796 Trade Creditors)
    --   VAT_CONTROL          input + output VAT meet here (817 VAT)
    --   USER_ACCOUNT         money owed to/from a member (900-x; e.g. 905 expense payments)
    --   OPENING_EQUITY       contra for a bank opening balance
    --   EXPLANATION_CATEGORY the category the user picked on a bank explanation
    --   SOURCE_CATEGORY      the bill/expense line's category
    --   SALES_DEFAULT        default income account (001 Sales) — until invoices
    --                        carry per-line categories
    --   TRANSFER_SOURCE_BANK / TRANSFER_DEST_BANK  the two sides of a bank transfer
    --   SUSPENSE             holding account (999) for not-yet-built entity types
    --                        (credit notes, HP agreements) — provisional, flagged
    --   PAYROLL_*            the payroll-run accrual legs (PAYROLL_COMPLETED event):
    --                          PAYROLL_GROSS_EXPENSE          gross wages P&L (257)
    --                          PAYROLL_EMPLOYER_NI_EXPENSE    employer NI P&L (214)
    --                          PAYROLL_EMPLOYER_PENSION_EXPENSE employer pension P&L (246)
    --                          PAYE_NI_LIABILITY              PAYE + NI owed to HMRC (814)
    --                          PENSION_LIABILITY              pension owed to provider (813)
    --                          STUDENT_LOAN_LIABILITY         student loan owed (815)
    --                          NET_PAY_PAYABLE                net pay owed to the employee
    --                                                         (902, per-employee sub-account)
    --                          OTHER_PAYROLL_DEDUCTIONS       other deductions creditor (815)
    --   PAYROLL_DIRECTOR_*    director variants of the three employer-cost expense legs
    --                          (407/408/409); staff use the plain PAYROLL_*_EXPENSE (401/402/403)
    account_role         VARCHAR(50) NOT NULL CHECK (account_role IN (
                            'BANK','DEBTORS','CREDITORS','VAT_CONTROL','USER_ACCOUNT',
                            'OPENING_EQUITY','EXPLANATION_CATEGORY','SOURCE_CATEGORY',
                            'SALES_DEFAULT','TRANSFER_SOURCE_BANK','TRANSFER_DEST_BANK',
                            'SUSPENSE',
                            'PAYROLL_GROSS_EXPENSE','PAYROLL_EMPLOYER_NI_EXPENSE',
                            'PAYROLL_EMPLOYER_PENSION_EXPENSE','PAYE_NI_LIABILITY',
                            'PENSION_LIABILITY','STUDENT_LOAN_LIABILITY','NET_PAY_PAYABLE',
                            'OTHER_PAYROLL_DEDUCTIONS',
                            'PAYROLL_DIRECTOR_GROSS_EXPENSE','PAYROLL_DIRECTOR_EMPLOYER_NI_EXPENSE',
                            'PAYROLL_DIRECTOR_EMPLOYER_PENSION_EXPENSE')),

    -- Which money component of the source row this leg takes. The interpreter reads
    -- the ALREADY-COMPUTED values off the source — it never re-does the arithmetic.
    -- GROSS = NET + VAT (so an entry of NET+VAT vs GROSS balances). The payroll bases
    -- (GROSS_PAY … NET_PAY) read the matching payslip column(s); see PAYROLL_COMPLETED.
    amount_basis         VARCHAR(20) NOT NULL CHECK (amount_basis IN (
                            'GROSS','NET','VAT',
                            'GROSS_PAY','PAYE','EMPLOYEE_NI','EMPLOYER_NI',
                            'EMPLOYEE_PENSION','EMPLOYER_PENSION','STUDENT_LOAN',
                            'NET_PAY','OTHER_DEDUCTIONS')),

    -- Debit or credit. Interpreter posts +amount (DR) or −amount (CR). A leg whose
    -- resolved amount is 0 (e.g. the VAT leg on a zero-VAT line) is dropped.
    direction            VARCHAR(2)  NOT NULL CHECK (direction IN ('DR','CR')),

    -- Branch a rule by org type where the legs genuinely differ (else 'ALL'), like
    -- transaction_type_categories.company_type. Today every rule is 'ALL' (the
    -- Ltd/sole-trader differences are in WHICH nominal the picked category is, not
    -- in the Dr/Cr shape).
    company_type         VARCHAR(20) NOT NULL DEFAULT 'ALL' CHECK (company_type IN (
                            'ALL','limited','sole_trader','partnership','landlord','corporation')),

    -- TRUE = the poster emits ONE line PER source sub-entity instead of a single
    -- aggregate line. Used by the payroll accrual's NET_PAY_PAYABLE leg: one credit
    -- per payslip, resolved to that employee's 902-x sub-account. FALSE = one line
    -- summing the basis across the run (the expense + liability legs).
    per_employee         BOOLEAN     NOT NULL DEFAULT FALSE,

    -- Which payslips feed this leg (payroll accrual): the poster restricts the
    -- amount_basis sum (or the per_employee fan-out) to payslips matching it.
    -- DIRECTOR = payslips.nic_calculation != 'employee'; STAFF = = 'employee';
    -- ALL = every payslip. Lets the employer-cost expense legs split director (407/
    -- 408/409) vs staff (401/402/403) while liability + net-pay legs stay ALL.
    employee_filter      VARCHAR(10) NOT NULL DEFAULT 'ALL' CHECK (employee_filter IN ('ALL','DIRECTOR','STAFF')),

    description_template VARCHAR(200),                         -- optional line-narrative template
    display_order        INTEGER     NOT NULL DEFAULT 0,
    created_at           TIMESTAMPTZ NOT NULL DEFAULT now(),

    -- One leg per (event, leg_no, company_type) — the natural key + the ON CONFLICT
    -- key for the idempotent seed.
    CONSTRAINT uq_gl_posting_rules UNIQUE (event_code, leg_no, company_type)
);

-- Backs ListPostingRulesForEvent (event [+ company_type] → its legs, in order).
CREATE INDEX idx_gl_posting_rules_event ON gl_posting_rules (event_code, company_type);


-- -----------------------------------------------------------------------------
-- gl_account_roles
-- Maps a FIXED control account_role (DEBTORS, VAT_CONTROL, …) to the nominal_code
-- it resolves to. GLOBAL reference data that SOFT-links categories by nominal_code
-- (no FK across the global/per-org boundary), resolved against the caller's org at
-- post time — exactly like transaction_type_categories. company_type lets a role
-- differ by org type (e.g. the user current account is the Director's Loan Account
-- for a Ltd, Drawings for a sole trader); 'ALL' = every org type.
--
-- Only the FIXED roles live here. The entity-derived roles resolve from the event's
-- own links, NOT this table:
--   EXPLANATION_CATEGORY / SOURCE_CATEGORY → the category already on the source row
--   BANK / TRANSFER_*_BANK                 → the bank account's own ledger account
--                                            (per-account; lands with bank sub-accounts)
-- USER_ACCOUNT IS here (→ a parent like 907) but is itself is_user_subdivided, so the
-- resolver then expands it to the linked user's sub-account.
--
-- OVERRIDABLE per ORG and per COUNTRY. A row's scope is set by organisation_id +
-- country_code (both NULL = the global default). The resolver picks the MOST SPECIFIC
-- match, mirroring the company_type 'specific-over-ALL' rule:
--     org-specific  →  country-specific  →  company_type-specific  →  global default.
-- This lets one org renumber its chart, or a whole country use a different nominal
-- scheme, without touching gl_posting_rules or the resolver code — data only.
-- organisation_id is a PLAIN column (no FK): this table soft-links everything (like
-- nominal_code → categories), which keeps the ledger sqlc block self-contained.
-- -----------------------------------------------------------------------------
CREATE TABLE gl_account_roles (
    role            VARCHAR(50) NOT NULL,                          -- DEBTORS, CREDITORS, VAT_CONTROL, USER_ACCOUNT, PAYROLL_*, …
    nominal_code    VARCHAR(20) NOT NULL,                          -- soft-link to categories.nominal_code (per org)
    organisation_id UUID,                                          -- NULL = not org-specific (a global / country default)
    country_code    CHAR(2),                                       -- NULL = all countries; else this ISO country
    company_type    VARCHAR(20) NOT NULL DEFAULT 'ALL' CHECK (company_type IN (
                        'ALL','limited','sole_trader','partnership','landlord','corporation')),
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),

    -- One nominal per (role, scope). NULLS NOT DISTINCT (PG15+) treats the NULL scopes
    -- as equal so the idempotent ON CONFLICT seed of the global rows behaves.
    CONSTRAINT uq_gl_account_roles UNIQUE NULLS NOT DISTINCT (role, organisation_id, country_code, company_type)
);


-- =============================================================================
-- COMMENTS
-- =============================================================================
COMMENT ON TABLE  gl_posting_rules IS 'The double-entry mapping AS DATA: per economic event, the journal legs (account role + money component + Dr/Cr). GLOBAL reference (no organisation_id), like transaction_types. A generic interpreter reads these to post balanced journal entries; validated against FreeAgent''s chart/trial balance.';
COMMENT ON TABLE  gl_account_roles IS 'Maps a fixed control account_role (DEBTORS, VAT_CONTROL, USER_ACCOUNT, …) to the nominal_code it posts to, soft-linked to per-org categories by nominal_code. Overridable per organisation_id / country_code (both NULL = global default); the resolver picks the most specific match: org → country → company_type → global. Entity-derived roles (EXPLANATION_CATEGORY, BANK) are NOT here — they resolve from the event''s links.';
COMMENT ON COLUMN gl_posting_rules.event_code   IS 'Bank explanations: == transaction_types.code (PAYMENT, SALES, …). Non-bank: synthetic (EXPENSE_APPROVED, INVOICE_SENT, BILL_CREATED, BANK_OPENING). Free text — no FK (synthetic codes have no transaction_types row).';
COMMENT ON COLUMN gl_posting_rules.account_role IS 'Symbolic posting target resolved to a categories row at post time (BANK, DEBTORS, CREDITORS, VAT_CONTROL, USER_ACCOUNT, EXPLANATION_CATEGORY, …). EXPLANATION_CATEGORY/SOURCE_CATEGORY resolve to the live picked category.';
COMMENT ON COLUMN gl_posting_rules.amount_basis IS 'Which already-computed money component this leg takes: GROSS, NET or VAT. GROSS = NET + VAT, so an entry of (NET + VAT) against GROSS balances.';
COMMENT ON COLUMN gl_posting_rules.direction    IS 'DR or CR. The interpreter posts +amount for DR, −amount for CR; the legs of an event sum to zero. A leg resolving to amount 0 (no VAT) is dropped.';
