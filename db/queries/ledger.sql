-- =============================================================================
-- GENERAL LEDGER — QUERIES (sqlc input)
-- Generated into package `ledger` at db/ledger (see sqlc.yaml).
--
-- ITERATION 1: read access to the posting-rules reference table only. The
-- interpreter (poster) and the journal-entry writes land in a later iteration.
-- =============================================================================

-- name: ListPostingRules :many
-- All posting rules, ordered for a deterministic read (event, then leg). Backs
-- the offline structural/cross-check tests and any "show me the mapping" view.
SELECT event_code, leg_no, account_role, amount_basis, direction,
       company_type, per_employee, employee_filter, description_template, display_order
FROM gl_posting_rules
ORDER BY event_code, company_type, leg_no;

-- name: ListPostingRulesForEvent :many
-- The legs of ONE event for an org's company_type: the org-specific rule if one
-- exists, else the 'ALL' default. This is the lookup the interpreter will use to
-- build a journal entry. Ordered by leg_no so the legs come back in posting order.
SELECT event_code, leg_no, account_role, amount_basis, direction,
       company_type, per_employee, employee_filter, description_template, display_order
FROM gl_posting_rules
WHERE event_code = $1
  AND company_type IN ('ALL', sqlc.arg(company_type))
ORDER BY
    -- Prefer the company-type-specific leg over the 'ALL' default for the same leg_no.
    leg_no,
    CASE WHEN company_type = 'ALL' THEN 1 ELSE 0 END;

-- name: GetAccountRoleNominal :one
-- The nominal_code a FIXED control role resolves to, picking the MOST SPECIFIC scope
-- that matches the caller: org-specific → country-specific → company_type-specific →
-- global default. organisation_id / country_code IS NULL = a broader (global/country)
-- default. The resolver then looks the returned nominal up in the caller's org
-- categories. An empty country_code arg matches only the country-agnostic rows.
SELECT nominal_code
FROM gl_account_roles
WHERE role = $1
  AND (organisation_id = sqlc.arg(organisation_id) OR organisation_id IS NULL)
  AND (country_code    = sqlc.arg(country_code)    OR country_code    IS NULL)
  AND (company_type    = sqlc.arg(company_type)    OR company_type = 'ALL')
ORDER BY
    (organisation_id IS NOT NULL) DESC,   -- org override beats everything
    (country_code    IS NOT NULL) DESC,   -- then a country override
    (company_type <> 'ALL')       DESC    -- then a company_type-specific default
LIMIT 1;


-- -----------------------------------------------------------------------------
-- JOURNAL ENTRIES + LINES (the poster's write path)
-- The poster replaces any prior entry for a source event (DeleteJournalEntryForSource,
-- lines cascade) then writes a fresh entry + its lines. Σ base_amount_minor = 0 is
-- enforced by the deferred constraint trigger (trg_gl_entry_balanced).
-- -----------------------------------------------------------------------------

-- name: DeleteJournalEntryForSource :exec
-- Remove the live entry for a source event (its lines cascade). Idempotent replace.
DELETE FROM gl_journal_entries
WHERE organisation_id = $1
  AND source_type     = sqlc.arg(source_type)
  AND source_id       = sqlc.arg(source_id)
  AND NOT is_reversal;

-- name: CreateJournalEntry :one
INSERT INTO gl_journal_entries (
    organisation_id, entry_date, base_currency, narrative,
    source_type, source_id, created_by_user_id
) VALUES (
    $1, sqlc.arg(entry_date), sqlc.arg(base_currency), sqlc.arg(narrative),
    sqlc.arg(source_type), sqlc.arg(source_id), sqlc.arg(created_by_user_id)
)
RETURNING id;

-- name: CreateJournalLine :exec
INSERT INTO gl_journal_lines (
    journal_entry_id, organisation_id, account_id,
    currency, amount_minor, base_amount_minor, exchange_rate,
    contact_id, project_id, user_id, description
) VALUES (
    $1, $2, sqlc.arg(account_id),
    sqlc.arg(currency), sqlc.arg(amount_minor), sqlc.arg(base_amount_minor), sqlc.arg(exchange_rate),
    sqlc.arg(contact_id), sqlc.arg(project_id), sqlc.arg(user_id), sqlc.arg(description)
);

-- name: ListLinesForEntry :many
-- Backs tests + the future account-ledger drill-down.
SELECT id, journal_entry_id, organisation_id, account_id,
       currency, amount_minor, base_amount_minor, exchange_rate,
       contact_id, project_id, user_id, description, created_at
FROM gl_journal_lines
WHERE journal_entry_id = $1
ORDER BY id;

-- name: GetJournalEntryForSource :one
-- The live entry for a source event (for tests / mutation checks).
SELECT id, organisation_id, entry_date, base_currency, narrative,
       source_type, source_id, is_reversal, reverses_entry_id, created_by_user_id, created_at
FROM gl_journal_entries
WHERE organisation_id = $1
  AND source_type     = sqlc.arg(source_type)
  AND source_id       = sqlc.arg(source_id)
  AND NOT is_reversal;


-- -----------------------------------------------------------------------------
-- REPORTING (read models over the posted ledger)
-- -----------------------------------------------------------------------------

-- name: GetTrialBalance :many
-- Trial balance as of a date: every CoA account with >=1 journal line on/before the
-- date, with its net signed balance (DR +, CR -) in the org base currency. Reversal
-- entries ARE included (they are real lines and keep the books balanced — excluding
-- them would unbalance the report). Zero-net accounts that still HAVE lines appear
-- (the GROUP BY only emits accounts with lines), so there is no HAVING filter.
-- Ordered by nominal_code (text order is correct for the zero-padded codes; a
-- sub-account like '750-1' sorts right after its '750' parent). Σ over all rows is
-- zero (the DB balance trigger guarantees it), so total debit == total credit.
SELECT c.nominal_code,
       c.name,
       c.account_type,
       SUM(l.base_amount_minor)::bigint AS balance_minor
FROM gl_journal_lines l
JOIN gl_journal_entries e ON e.id = l.journal_entry_id
JOIN categories c ON c.id = l.account_id
WHERE l.organisation_id = $1
  AND e.entry_date <= sqlc.arg(as_of_date)
GROUP BY c.nominal_code, c.name, c.account_type
ORDER BY c.nominal_code;

-- name: GetAccountTransactions :many
-- The general-ledger lines posted to ONE account (by nominal_code) for an org, over
-- an OPTIONAL date range (from_date NULL = open lower bound). Signed base_amount_minor
-- (DR +, CR -) drives the Debit/Credit split in Go. Reversal lines ARE included (they
-- are real lines). Ordered chronologically (entry_date, then insertion order) so the
-- report reads top-to-bottom in time. Backs the Account Transactions report (the
-- trial-balance drill-down).
SELECT e.entry_date,
       e.narrative,
       e.source_type,
       e.source_id,
       l.base_amount_minor,
       l.description
FROM gl_journal_lines l
JOIN gl_journal_entries e ON e.id = l.journal_entry_id
JOIN categories c ON c.id = l.account_id
WHERE l.organisation_id = $1
  AND c.nominal_code = sqlc.arg(nominal_code)
  AND (sqlc.narg(from_date)::date IS NULL OR e.entry_date >= sqlc.narg(from_date))
  AND e.entry_date <= sqlc.arg(to_date)
ORDER BY e.entry_date, e.created_at;
