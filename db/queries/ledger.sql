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
