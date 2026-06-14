-- =============================================================================
-- EXPENSES MODULE — SQLC QUERIES
-- File: db/queries/query.sql
--
-- What is sqlc?
--   sqlc reads this file plus your schema.sql and generates type-safe Go code.
--   Every query here becomes a Go function with typed parameters and return
--   values. You never write boilerplate database scan/row code by hand.
--
-- Annotation syntax:
--   Every query must have a comment in this exact format:
--
--     -- name: FunctionName :return_type
--
--   Return types:
--     :one   → returns a single row (error if 0 or >1 rows)
--     :many  → returns a slice of rows
--     :exec  → returns only an error (for INSERT/UPDATE/DELETE with no rows back)
--     :execresult → returns sql.Result + error (gives you rows affected)
--
-- Parameter syntax:
--   $1, $2, $3 ... are positional parameters. sqlc maps these to typed Go
--   function arguments in order. So $1 becomes the first argument, $2 the
--   second, etc. The types are inferred from your schema columns.
--
-- sqlpkg: pgx/v5
--   We use pgx/v5 not database/sql. pgx is faster, supports more PostgreSQL
--   types natively (e.g. pgtype.UUID, pgtype.Numeric), and has better
--   context/timeout support.
-- =============================================================================


-- =============================================================================
-- SECTION 1: EXPENSES — CORE CRUD
-- =============================================================================

-- -----------------------------------------------------------------------------
-- CreateExpense
-- Inserts a new expense row. Returns the full created row immediately via
-- RETURNING so the caller gets the generated id, created_at, and updated_at
-- without a second round-trip to the database.
--
-- Note on money: all _minor parameters are INTEGER (pence). The application
-- layer converts from pounds/pence before calling this.
-- Note on status: defaults to 'DRAFT' in the schema, so we don't pass it here.
-- -----------------------------------------------------------------------------
-- name: CreateExpense :one
INSERT INTO expenses (
    organisation_id,
    user_id,
    created_by_user_id,
    category_id,
    dated_on,
    description,
    receipt_reference,
    invoice_number,
    supplier_name,
    supplier_vat_number,
    currency,
    native_currency,
    exchange_rate,
    gross_value_minor,
    native_gross_value_minor,
    vat_rate_id,
    vat_rate_bps,
    vat_value_minor,
    native_vat_value_minor,
    manual_vat_amount_minor,
    vat_status,
    ec_status,
    project_id,
    rebill_type,
    rebill_factor,
    stock_item_id,
    stock_item_description,
    stock_quantity,
    property_id
) VALUES (
    $1,   -- organisation_id       UUID
    $2,   -- user_id               UUID  (the claimant)
    $3,   -- created_by_user_id    UUID  (may differ from claimant if admin entering on behalf)
    $4,   -- category_id           UUID
    $5,   -- dated_on              DATE  (date on the receipt)
    $6,   -- description           TEXT
    $7,   -- receipt_reference     VARCHAR (nullable — use NULL if none)
    $8,   -- invoice_number        VARCHAR (nullable — supplier's invoice number)
    $9,   -- supplier_name         VARCHAR (nullable)
    $10,  -- supplier_vat_number   VARCHAR (nullable)
    $11,  -- currency              CHAR(3) e.g. 'GBP'
    $12,  -- native_currency       CHAR(3) e.g. 'GBP'
    $13,  -- exchange_rate         NUMERIC (nullable — NULL if same currency)
    $14,  -- gross_value_minor     INTEGER (pence, negative = owed to employee)
    $15,  -- native_gross_value_minor INTEGER (pence in home currency)
    $16,  -- vat_rate_id           UUID (nullable — NULL if no VAT)
    $17,  -- vat_rate_bps          INTEGER (nullable — snapshot of rate, e.g. 2000 = 20%)
    $18,  -- vat_value_minor       INTEGER (pence)
    $19,  -- native_vat_value_minor INTEGER (pence)
    $20,  -- manual_vat_amount_minor INTEGER (nullable — override for foreign currency expenses)
    $21,  -- vat_status            VARCHAR 'TAXABLE'|'EXEMPT'|'OUT_OF_SCOPE'
    $22,  -- ec_status             VARCHAR 'UK_NON_EC'|'EC_GOODS'|'EC_SERVICES'|'REVERSE_CHARGE'
    $23,  -- project_id            UUID (nullable)
    $24,  -- rebill_type           VARCHAR (nullable) 'cost'|'markup'|'price'
    $25,  -- rebill_factor         NUMERIC (nullable)
    $26,  -- stock_item_id         UUID (nullable)
    $27,  -- stock_item_description TEXT (nullable)
    $28,  -- stock_quantity        NUMERIC (nullable)
    $29   -- property_id           UUID (nullable)
)
RETURNING *;
-- ^ RETURNING * tells sqlc to map the full expenses row as the return type.
--   sqlc will generate an Expense struct with all columns as fields.


-- -----------------------------------------------------------------------------
-- GetExpense
-- Fetch a single expense by its UUID, scoped to the organisation.
-- We ALWAYS scope by organisation_id — never fetch by id alone.
-- This prevents one tenant accidentally reading another's data if an id leaks.
-- deleted_at IS NULL ensures soft-deleted records are invisible.
-- -----------------------------------------------------------------------------
-- name: GetExpense :one
SELECT * FROM expenses
WHERE id              = $1   -- expense UUID
  AND organisation_id = $2   -- tenant scope — never skip this
  AND deleted_at IS NULL;    -- soft delete filter


-- -----------------------------------------------------------------------------
-- GetExpenseWithDetails
-- Fetches an expense joined with its category name and nominal code.
-- Uses the v_expenses_full view we defined in the schema.
-- This is the query you'd use for the "view expense" detail page.
-- -----------------------------------------------------------------------------
-- name: GetExpenseWithDetails :one
SELECT * FROM v_expenses_full
WHERE id              = $1
  AND organisation_id = $2;


-- -----------------------------------------------------------------------------
-- ListExpenses
-- Returns all active expenses for an organisation, newest first.
-- This is the base query for the expenses list page with no filters.
-- -----------------------------------------------------------------------------
-- name: ListExpenses :many
SELECT * FROM expenses
WHERE organisation_id = $1
  AND deleted_at IS NULL
ORDER BY dated_on DESC, created_at DESC;


-- -----------------------------------------------------------------------------
-- ListExpensesByUser
-- Expenses for a specific claimant (the "my expenses" view).
-- Useful for employee self-service: they only see their own claims.
-- -----------------------------------------------------------------------------
-- name: ListExpensesByUser :many
SELECT * FROM expenses
WHERE organisation_id = $1
  AND user_id         = $2
  AND deleted_at IS NULL
ORDER BY dated_on DESC, created_at DESC;


-- -----------------------------------------------------------------------------
-- ListExpensesByDateRange
-- Filter by date range — used on the expenses list page date picker.
-- Both from_date and to_date are inclusive (BETWEEN is inclusive in PostgreSQL).
-- -----------------------------------------------------------------------------
-- name: ListExpensesByDateRange :many
SELECT * FROM expenses
WHERE organisation_id = $1
  AND dated_on BETWEEN $2 AND $3   -- $2 = from_date, $3 = to_date
  AND deleted_at IS NULL
ORDER BY dated_on DESC;


-- -----------------------------------------------------------------------------
-- ListExpensesByStatus
-- Filter by workflow status — e.g. fetch all 'SUBMITTED' expenses for the
-- manager approval queue.
-- -----------------------------------------------------------------------------
-- name: ListExpensesByStatus :many
SELECT * FROM expenses
WHERE organisation_id = $1
  AND status          = $2   -- 'DRAFT'|'SUBMITTED'|'APPROVED'|'REJECTED'|'PAID'
  AND deleted_at IS NULL
ORDER BY submitted_at ASC;   -- oldest first so managers action the earliest claims first


-- -----------------------------------------------------------------------------
-- ListExpensesByProject
-- All expenses linked to a specific project (for rebilling to a client).
-- -----------------------------------------------------------------------------
-- name: ListExpensesByProject :many
SELECT * FROM expenses
WHERE organisation_id = $1
  AND project_id      = $2
  AND deleted_at IS NULL
ORDER BY dated_on DESC;


-- -----------------------------------------------------------------------------
-- ListExpensesUpdatedSince
-- Returns all expenses modified after a given timestamp.
-- Used for sync / webhook scenarios: "give me everything changed since X".
-- Note: we do NOT filter deleted_at here — we want to surface soft-deleted
-- records so the consumer can remove them from their local cache too.
-- -----------------------------------------------------------------------------
-- name: ListExpensesUpdatedSince :many
SELECT * FROM expenses
WHERE organisation_id = $1
  AND updated_at      > $2   -- strict greater-than (caller passes their last-sync timestamp)
ORDER BY updated_at ASC;


-- -----------------------------------------------------------------------------
-- ListRecentExpenses
-- Last 30 days of expenses — useful for the dashboard "recent activity" widget.
-- -----------------------------------------------------------------------------
-- name: ListRecentExpenses :many
SELECT * FROM expenses
WHERE organisation_id = $1
  AND dated_on        >= CURRENT_DATE - INTERVAL '30 days'
  AND deleted_at IS NULL
ORDER BY dated_on DESC, created_at DESC;


-- -----------------------------------------------------------------------------
-- UpdateExpense
-- Updates the editable fields on an expense. Only allowed when status is
-- DRAFT or REJECTED (the application layer enforces this, not the DB).
-- We update updated_at explicitly here even though the trigger also does it —
-- belt-and-suspenders approach.
-- RETURNING * gives us the updated row back so the API can return it.
-- -----------------------------------------------------------------------------
-- name: UpdateExpense :one
UPDATE expenses SET
    category_id              = $3,
    dated_on                 = $4,
    description              = $5,
    receipt_reference        = $6,
    invoice_number           = $7,
    supplier_name            = $8,
    supplier_vat_number      = $9,
    currency                 = $10,
    native_currency          = $11,
    exchange_rate            = $12,
    gross_value_minor        = $13,
    native_gross_value_minor = $14,
    vat_rate_id              = $15,
    vat_rate_bps             = $16,
    vat_value_minor          = $17,
    native_vat_value_minor   = $18,
    manual_vat_amount_minor  = $19,
    vat_status               = $20,
    ec_status                = $21,
    project_id               = $22,
    rebill_type              = $23,
    rebill_factor            = $24,
    stock_item_id            = $25,
    stock_item_description   = $26,
    stock_quantity           = $27,
    property_id              = $28,
    updated_at               = now()
WHERE id              = $1   -- expense UUID
  AND organisation_id = $2   -- tenant scope
  AND deleted_at IS NULL     -- can't update a deleted record
RETURNING *;


-- -----------------------------------------------------------------------------
-- UpdateExpenseStatus
-- Dedicated query for status transitions only.
-- Separating this from UpdateExpense prevents accidental overwrite of all
-- fields during a status change (e.g. manager approving shouldn't change amounts).
-- The application layer validates the transition is legal before calling this.
-- -----------------------------------------------------------------------------
-- name: UpdateExpenseStatus :one
UPDATE expenses SET
    status               = $3,
    -- Set the relevant timestamp based on the new status
    -- We set all three here; the application passes NULL for the ones not relevant
    submitted_at         = $4,   -- set when status becomes SUBMITTED
    approved_at          = $5,   -- set when status becomes APPROVED
    approved_by_user_id  = $6,   -- set when status becomes APPROVED
    paid_at              = $7,   -- set when status becomes PAID
    rejection_note       = $8,   -- set when status becomes REJECTED
    updated_at           = now()
WHERE id              = $1
  AND organisation_id = $2
  AND deleted_at IS NULL
RETURNING *;


-- -----------------------------------------------------------------------------
-- SoftDeleteExpense
-- Sets deleted_at to mark the record as deleted. The row remains in the DB
-- for audit purposes but is invisible to all other queries.
-- Accounting records should never be hard-deleted once submitted.
-- :exec means the generated Go function returns only an error (no row back).
-- -----------------------------------------------------------------------------
-- name: SoftDeleteExpense :exec
UPDATE expenses SET
    deleted_at = now(),
    updated_at = now()
WHERE id              = $1
  AND organisation_id = $2
  AND deleted_at IS NULL;   -- idempotent: deleting an already-deleted record is a no-op


-- =============================================================================
-- SECTION 2: EXPENSE MILEAGE
-- =============================================================================

-- -----------------------------------------------------------------------------
-- CreateExpenseMileage
-- Inserts the mileage sub-record for a mileage claim.
-- Always called immediately after CreateExpense when category is_mileage = true.
-- The expense_id here is the UUID returned by CreateExpense.
-- -----------------------------------------------------------------------------
-- name: CreateExpenseMileage :one
INSERT INTO expense_mileage (
    expense_id,
    miles,
    journey_description,
    journey_from,
    journey_to,
    vehicle_type,
    engine_type,
    engine_size,
    reclaim_mileage,
    initial_rate_ppm,
    reduced_rate_ppm,
    rebill_rate_ppm,
    reimbursement_minor,
    have_vat_receipt
) VALUES (
    $1,   -- expense_id           UUID (from CreateExpense)
    $2,   -- miles                NUMERIC e.g. 42.5
    $3,   -- journey_description  TEXT (nullable)
    $4,   -- journey_from         VARCHAR (nullable)
    $5,   -- journey_to           VARCHAR (nullable)
    $6,   -- vehicle_type         'CAR'|'MOTORCYCLE'|'BICYCLE'
    $7,   -- engine_type          VARCHAR (nullable for bicycles)
    $8,   -- engine_size          VARCHAR (nullable)
    $9,   -- reclaim_mileage      BOOLEAN (true = claim AMAP from HMRC)
    $10,  -- initial_rate_ppm     INTEGER (pence-per-mile, e.g. 45 = 45p/mile)
    $11,  -- reduced_rate_ppm     INTEGER (pence-per-mile above 10k threshold, e.g. 25)
    $12,  -- rebill_rate_ppm      INTEGER (nullable — ppm to charge client)
    $13,  -- reimbursement_minor  INTEGER (total reimbursement in pence, computed by service)
    $14   -- have_vat_receipt     BOOLEAN
)
RETURNING *;


-- -----------------------------------------------------------------------------
-- GetExpenseMileage
-- Fetch the mileage sub-record for a given expense.
-- Called when rendering the mileage claim detail view.
-- -----------------------------------------------------------------------------
-- name: GetExpenseMileage :one
SELECT * FROM expense_mileage
WHERE expense_id = $1;


-- -----------------------------------------------------------------------------
-- UpdateExpenseMileage
-- Update mileage fields — only valid while parent expense is DRAFT/REJECTED.
-- -----------------------------------------------------------------------------
-- name: UpdateExpenseMileage :one
UPDATE expense_mileage SET
    miles                = $2,
    journey_description  = $3,
    journey_from         = $4,
    journey_to           = $5,
    vehicle_type         = $6,
    engine_type          = $7,
    engine_size          = $8,
    reclaim_mileage      = $9,
    initial_rate_ppm     = $10,
    reduced_rate_ppm     = $11,
    rebill_rate_ppm      = $12,
    reimbursement_minor  = $13,
    have_vat_receipt     = $14,
    updated_at           = now()
WHERE expense_id = $1
RETURNING *;


-- =============================================================================
-- SECTION 3: EXPENSE ATTACHMENTS
-- =============================================================================

-- -----------------------------------------------------------------------------
-- CreateExpenseAttachment
-- Records metadata for an uploaded file. The actual file has already been
-- written to GCS by the time this is called. storage_path is the GCS object
-- path — never a signed URL (those are generated on-demand, not stored).
-- -----------------------------------------------------------------------------
-- name: CreateExpenseAttachment :one
INSERT INTO expense_attachments (
    expense_id,
    organisation_id,
    file_name,
    content_type,
    file_size_bytes,
    storage_path,
    storage_bucket,
    description,
    is_primary,
    uploaded_by_user_id
) VALUES (
    $1,   -- expense_id           UUID
    $2,   -- organisation_id      UUID
    $3,   -- file_name            VARCHAR e.g. 'receipt_jan.pdf'
    $4,   -- content_type         VARCHAR e.g. 'application/pdf'
    $5,   -- file_size_bytes      INTEGER
    $6,   -- storage_path         TEXT e.g. 'orgs/abc123/expenses/xyz/receipt.pdf'
    $7,   -- storage_bucket       VARCHAR e.g. 'myapp-expense-documents-prod'
    $8,   -- description          TEXT (nullable — user label for this file)
    $9,   -- is_primary           BOOLEAN
    $10   -- uploaded_by_user_id  UUID
)
RETURNING *;


-- -----------------------------------------------------------------------------
-- ListExpenseAttachments
-- All attachments for one expense, primary file first.
-- The UI shows attachments in this order.
-- -----------------------------------------------------------------------------
-- name: ListExpenseAttachments :many
SELECT * FROM expense_attachments
WHERE expense_id = $1
ORDER BY is_primary DESC, created_at ASC;
-- is_primary DESC: TRUE sorts before FALSE, so primary comes first


-- -----------------------------------------------------------------------------
-- GetExpenseAttachment
-- Single attachment by id — used when generating a signed download URL.
-- We scope by organisation_id to prevent cross-tenant access.
-- -----------------------------------------------------------------------------
-- name: GetExpenseAttachment :one
SELECT * FROM expense_attachments
WHERE id              = $1
  AND organisation_id = $2;


-- -----------------------------------------------------------------------------
-- UpdateAttachmentOCRStatus
-- Called by the background OCR processing pipeline to record results.
-- ocr_extracted_data is JSONB — in Go this will be a []byte that your service
-- deserialises into a struct (e.g. ExtractedExpenseData).
-- -----------------------------------------------------------------------------
-- name: UpdateAttachmentOCRStatus :one
UPDATE expense_attachments SET
    ocr_status         = $2,   -- 'PROCESSING'|'COMPLETE'|'FAILED'
    ocr_raw_text       = $3,   -- full text from OCR engine (nullable)
    ocr_extracted_data = $4,   -- JSONB structured fields (nullable)
    ocr_processed_at   = now(),
    updated_at         = now()
WHERE id = $1
RETURNING *;


-- -----------------------------------------------------------------------------
-- DeleteExpenseAttachment
-- Hard delete — attachments can be genuinely removed (they're not financial
-- records themselves, just supporting documents). The file in GCS must be
-- deleted separately by the application layer after this succeeds.
-- :exec returns only an error.
-- -----------------------------------------------------------------------------
-- name: DeleteExpenseAttachment :exec
DELETE FROM expense_attachments
WHERE id              = $1
  AND organisation_id = $2;


-- -----------------------------------------------------------------------------
-- CountExpenseAttachments
-- How many files an expense already has. The service calls this when a new file
-- is uploaded: if the count is 0, this upload is the FIRST one and therefore
-- becomes the primary (default) attachment. Scoped by expense_id only — the
-- caller has already been authorised against the parent expense's organisation,
-- which is the same convention ListExpenseAttachments uses.
-- -----------------------------------------------------------------------------
-- name: CountExpenseAttachments :one
SELECT count(*) FROM expense_attachments
WHERE expense_id = $1;


-- -----------------------------------------------------------------------------
-- UnsetExpensePrimary
-- Clears the primary flag on every attachment of an expense. Run inside a
-- transaction immediately BEFORE SetAttachmentPrimary so that exactly one row
-- ends up flagged primary. organisation_id keeps the update tenant-scoped.
-- -----------------------------------------------------------------------------
-- name: UnsetExpensePrimary :exec
UPDATE expense_attachments SET
    is_primary = FALSE,
    updated_at = now()
WHERE expense_id      = $1
  AND organisation_id = $2
  AND is_primary      = TRUE;


-- -----------------------------------------------------------------------------
-- SetAttachmentPrimary
-- Marks a single attachment as the primary one for its expense. Pair it with
-- UnsetExpensePrimary inside one transaction so the "exactly one primary"
-- invariant holds. organisation_id prevents cross-tenant writes.
-- -----------------------------------------------------------------------------
-- name: SetAttachmentPrimary :exec
UPDATE expense_attachments SET
    is_primary = TRUE,
    updated_at = now()
WHERE id              = $1
  AND organisation_id = $2;


-- =============================================================================
-- SECTION 4: EXPENSE RECURRENCE
-- =============================================================================

-- -----------------------------------------------------------------------------
-- CreateExpenseRecurrence
-- Attaches a recurrence schedule to an expense template.
-- The parent expense row holds the financial values; this table holds the
-- schedule. The service layer reads next_recurs_on daily and creates new
-- expense rows as copies of the template.
-- -----------------------------------------------------------------------------
-- name: CreateExpenseRecurrence :one
INSERT INTO expense_recurrence (
    expense_id,
    organisation_id,
    frequency,
    next_recurs_on,
    end_date
) VALUES (
    $1,   -- expense_id      UUID (the template expense)
    $2,   -- organisation_id UUID
    $3,   -- frequency       e.g. 'MONTHLY', 'QUARTERLY'
    $4,   -- next_recurs_on  DATE (when the next copy should be created)
    $5    -- end_date        DATE (nullable — NULL means recur forever)
)
RETURNING *;


-- -----------------------------------------------------------------------------
-- GetExpenseRecurrence
-- Fetch recurrence settings for a given expense.
-- -----------------------------------------------------------------------------
-- name: GetExpenseRecurrence :one
SELECT * FROM expense_recurrence
WHERE expense_id = $1;


-- -----------------------------------------------------------------------------
-- ListDueRecurrences
-- Returns all active recurrences that are due today or overdue.
-- This is the query your cron job / scheduler runs daily to generate new
-- expense copies. It returns the recurrence rows; the service then fetches
-- the template expense and creates a new copy for each one.
-- -----------------------------------------------------------------------------
-- name: ListDueRecurrences :many
SELECT * FROM expense_recurrence
WHERE next_recurs_on <= CURRENT_DATE   -- due today or overdue
  AND is_active      = TRUE
  AND (end_date IS NULL OR end_date >= CURRENT_DATE)
ORDER BY next_recurs_on ASC;


-- -----------------------------------------------------------------------------
-- UpdateRecurrenceNextDate
-- After a recurrence fires and a new expense is created, advance the
-- next_recurs_on date. The service layer calculates the next date based on
-- frequency (e.g. add 1 month) and passes it here.
-- -----------------------------------------------------------------------------
-- name: UpdateRecurrenceNextDate :one
UPDATE expense_recurrence SET
    next_recurs_on = $2,
    updated_at     = now()
WHERE expense_id = $1
RETURNING *;


-- -----------------------------------------------------------------------------
-- DeactivateExpenseRecurrence
-- Stops a recurrence. Sets is_active = false rather than deleting the row
-- so we retain the history of when it was active.
-- -----------------------------------------------------------------------------
-- name: DeactivateExpenseRecurrence :exec
UPDATE expense_recurrence SET
    is_active  = FALSE,
    updated_at = now()
WHERE expense_id      = $1
  AND organisation_id = $2;


-- =============================================================================
-- SECTION 5: REPORTING / AGGREGATION QUERIES
-- =============================================================================

-- -----------------------------------------------------------------------------
-- SumExpensesByCategory
-- Totals per category for a date range — used on the P&L and expense reports.
-- Returns category name, nominal code, and total gross + VAT in home currency.
-- These are INTEGER sums (pence); the service layer formats them for display.
-- -----------------------------------------------------------------------------
-- name: SumExpensesByCategory :many
SELECT
    ec.id               AS category_id,
    ec.nominal_code,
    ec.name             AS category_name,
    COUNT(e.id)         AS expense_count,
    -- SUM returns BIGINT when summing INTEGER columns — good, avoids overflow
    -- for organisations with many expenses. COALESCE handles the case where
    -- there are no matching rows (SUM of empty set = NULL, not 0).
    COALESCE(SUM(e.native_gross_value_minor), 0)  AS total_gross_minor,
    COALESCE(SUM(e.native_vat_value_minor), 0)    AS total_vat_minor
FROM expenses e
JOIN expense_categories ec ON ec.id = e.category_id
WHERE e.organisation_id = $1
  AND e.dated_on BETWEEN $2 AND $3
  AND e.deleted_at IS NULL
  AND e.status IN ('APPROVED', 'PAID')   -- only count approved/paid expenses in reports
GROUP BY ec.id, ec.nominal_code, ec.name
ORDER BY ec.nominal_code;


-- -----------------------------------------------------------------------------
-- SumExpensesByMonth
-- Month-by-month totals for a calendar year — used for trend charts.
-- DATE_TRUNC rounds each dated_on down to the first of its month, letting
-- GROUP BY aggregate by month cleanly.
-- -----------------------------------------------------------------------------
-- name: SumExpensesByMonth :many
SELECT
    DATE_TRUNC('month', dated_on)::DATE              AS month,
    COALESCE(SUM(native_gross_value_minor), 0)       AS total_gross_minor,
    COALESCE(SUM(native_vat_value_minor), 0)         AS total_vat_minor,
    COUNT(id)                                        AS expense_count
FROM expenses
WHERE organisation_id = $1
  AND dated_on        >= DATE_TRUNC('year', $2::DATE)    -- start of year
  AND dated_on        <  DATE_TRUNC('year', $2::DATE) + INTERVAL '1 year'
  AND deleted_at IS NULL
  AND status IN ('APPROVED', 'PAID')
GROUP BY DATE_TRUNC('month', dated_on)
ORDER BY month ASC;


-- -----------------------------------------------------------------------------
-- CountExpensesByStatus
-- Count of expenses per status — used for the dashboard summary cards.
-- e.g. "3 Pending Approval", "12 Paid This Month"
-- -----------------------------------------------------------------------------
-- name: CountExpensesByStatus :many
SELECT
    status,
    COUNT(*) AS count
FROM expenses
WHERE organisation_id = $1
  AND deleted_at IS NULL
GROUP BY status;


-- =============================================================================
-- SECTION 6: EXPENSE CATEGORIES (reference data)
-- =============================================================================

-- -----------------------------------------------------------------------------
-- ListExpenseCategories
-- All ACTIVE categories for an organisation, for the expense category picker.
-- Scoped by organisation_id — categories are per-tenant reference data.
-- Ordered by category_group then nominal_code so the UI can render stable
-- sections (Admin expenses / Assets and stock / Cost of Sales).
-- -----------------------------------------------------------------------------
-- name: ListExpenseCategories :many
SELECT * FROM expense_categories
WHERE organisation_id = $1
  AND is_active = TRUE
ORDER BY category_group, nominal_code;


-- =============================================================================
-- SECTION 7: VAT RATES (reference data)
-- =============================================================================

-- -----------------------------------------------------------------------------
-- ListVatRatesByCountry
-- All VAT rates that are valid TODAY for a given country, for the VAT rate
-- picker. VAT rates are global reference data keyed by country_code (not per
-- organisation) — the caller passes the organisation's country.
--
-- "Valid today" means the rate is in its effective window:
--   - effective_from is on or before today (not a future rate), AND
--   - effective_to is NULL (still active) or on/after today (not yet expired).
-- This is why the COVID 5% hospitality rate, for example, stops appearing once
-- its effective_to date has passed.
--
-- Explicit column list (not SELECT *) per project convention. Uses
-- idx_vat_rates_country for the country_code lookup.
-- -----------------------------------------------------------------------------
-- name: ListVatRatesByCountry :many
SELECT id, name, rate_bps, country_code, is_fixed_ratio, effective_from, effective_to, created_at
FROM vat_rates
WHERE country_code = $1
  AND effective_from <= CURRENT_DATE                        -- not yet in effect → excluded
  AND (effective_to IS NULL OR effective_to >= CURRENT_DATE) -- expired → excluded; NULL = still active
ORDER BY name;


-- -----------------------------------------------------------------------------
-- GetVatRate
-- Fetch a single VAT rate by id — used when applying VAT to an expense. The
-- service validates the rate's country_code against the caller's organisation,
-- then reads rate_bps + is_fixed_ratio to compute (fixed) or accept (custom) the
-- VAT amount. Deliberately NOT date-filtered: an expense may legitimately
-- reference a rate outside its current effective window (e.g. editing a
-- historical expense whose rate has since lapsed).
-- -----------------------------------------------------------------------------
-- name: GetVatRate :one
SELECT id, name, rate_bps, country_code, is_fixed_ratio, effective_from, effective_to, created_at
FROM vat_rates
WHERE id = $1;

