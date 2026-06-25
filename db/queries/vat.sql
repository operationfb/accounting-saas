-- =============================================================================
-- VAT MODULE — cross-domain read queries for the VAT-return calculation engine.
--
-- This is a READ-ONLY, reporting query set: it SELECTs the VAT-relevant rows from
-- four domains (expenses, invoices, bills, banking explanations) for a date range,
-- and the Go engine (internal/vat/calculate.go) routes each row into the 9 HMRC
-- boxes. All VAT SQL lives here in ONE place (generated into package `db/vat`,
-- imported as `vatdb`) rather than scattered across each domain's query file —
-- VAT is inherently a cross-domain report. The sqlc block (sqlc.yaml) loads every
-- schema these queries touch; nothing here writes, so there is no FK/write coupling.
--
-- These back the INVOICE/ACCRUAL basis: count the DOCUMENTS by their document date
-- (invoices SENT, bills, expenses APPROVED/PAID) plus DIRECT-CATEGORY bank
-- explanations (transaction_types.entity_link = 'NONE'). The bank transactions that
-- SETTLE invoices/bills/expenses (INVOICE_RECEIPT / BILL_PAYMENT / transfers / money
-- to-from a user — any entity_link <> 'NONE') are excluded, so VAT is never
-- double-counted. (The cash basis — settling bank transactions instead of the
-- documents — is a later slice.)
--
-- dated_on BETWEEN from AND to is INCLUSIVE of both period ends (a quarter is
-- [start, end] where end is the last day, e.g. 31 May). All queries are org-scoped
-- and exclude soft-deleted rows, mirroring ListExpensesByDateRange in query.sql.
-- =============================================================================


-- name: ListExpensesForVatReturn :many
-- INPUT VAT. Only APPROVED/PAID, human-confirmed (needs_review = FALSE) expenses
-- count. native_* are the home-currency (GBP pence) amounts; for a normal
-- out-of-pocket claim they are NEGATIVE (the engine negates them to a positive
-- purchase). vat_status / ec_status drive the box routing.
SELECT
    e.id,
    e.dated_on,
    e.description,
    e.supplier_name,
    ec.name AS category_name,
    e.native_gross_value_minor,
    e.native_vat_value_minor,
    e.vat_status,
    e.ec_status
FROM expenses e
JOIN expense_categories ec ON ec.id = e.category_id
WHERE e.organisation_id = sqlc.arg(organisation_id)
  AND e.dated_on BETWEEN sqlc.arg(from_date) AND sqlc.arg(to_date)
  AND e.deleted_at IS NULL
  AND e.needs_review = FALSE
  AND e.status IN ('APPROVED', 'PAID')
ORDER BY e.dated_on, e.id;


-- name: ListInvoicesForVatReturn :many
-- OUTPUT VAT. Only SENT (issued) invoices count; invoices carry no vat_status /
-- ec_status, so they are always UK-standard TAXABLE output. Amounts are positive.
SELECT
    i.id,
    i.dated_on,
    i.reference,
    i.net_value_minor,
    i.sales_tax_value_minor
FROM invoices i
WHERE i.organisation_id = sqlc.arg(organisation_id)
  AND i.dated_on BETWEEN sqlc.arg(from_date) AND sqlc.arg(to_date)
  AND i.deleted_at IS NULL
  AND i.status = 'SENT'
ORDER BY i.dated_on, i.id;


-- name: ListBillsForVatReturn :many
-- INPUT VAT. Bills have no status machine (live = not soft-deleted) and no
-- vat_status / ec_status, so they are always UK-standard TAXABLE input. Amounts are
-- positive for a normal bill (negative only for a credit note).
SELECT
    b.id,
    b.dated_on,
    b.reference,
    b.comments,
    b.net_value_minor,
    b.sales_tax_value_minor
FROM bills b
WHERE b.organisation_id = sqlc.arg(organisation_id)
  AND b.dated_on BETWEEN sqlc.arg(from_date) AND sqlc.arg(to_date)
  AND b.deleted_at IS NULL
ORDER BY b.dated_on, b.id;


-- name: ListExplanationsForVatReturn :many
-- Direct-category bank explanations only: the JOIN to transaction_types filters to
-- entity_link = 'NONE' (money booked straight to a category with its own VAT),
-- EXCLUDING the settlement types (INVOICE_RECEIPT / BILL_PAYMENT / TRANSFER / money
-- to-from a user) whose VAT already lives on the linked document. gross_value_minor
-- is SIGNED (+ money in = output / − money out = input); sales_tax_value_minor is a
-- positive magnitude. sales_tax_status / ec_status drive the routing.
SELECT
    x.id,
    x.dated_on,
    x.description,
    x.gross_value_minor,
    x.sales_tax_value_minor,
    x.sales_tax_status,
    x.ec_status
FROM bank_transaction_explanations x
JOIN transaction_types tt ON tt.code = x.type
WHERE x.organisation_id = sqlc.arg(organisation_id)
  AND x.dated_on BETWEEN sqlc.arg(from_date) AND sqlc.arg(to_date)
  AND x.deleted_at IS NULL
  AND tt.entity_link = 'NONE'
ORDER BY x.dated_on, x.id;


-- =============================================================================
-- CASH BASIS — invoices/bills are NOT counted as documents; instead the bank
-- transactions that SETTLE them are, with the document's VAT apportioned to the
-- amount that actually moved (the paid fraction). Expenses + direct-category bank
-- explanations are identical to the accrual basis, so the two queries above are
-- reused on cash too; only the two below replace the invoice/bill document queries.
-- =============================================================================

-- name: ListInvoiceReceiptsForVatReturn :many
-- CASH OUTPUT VAT. INVOICE_RECEIPT bank explanations (linked to the sales invoice
-- via paid_invoice_id) in the period; the engine apportions the invoice's VAT to
-- the received amount. gross_value_minor is the receipt (positive, money in).
SELECT
    x.id,
    x.dated_on,
    x.gross_value_minor,
    i.reference,
    i.sales_tax_value_minor AS invoice_vat_minor,
    i.total_value_minor     AS invoice_total_minor
FROM bank_transaction_explanations x
JOIN invoices i ON i.id = x.paid_invoice_id
WHERE x.organisation_id = sqlc.arg(organisation_id)
  AND x.dated_on BETWEEN sqlc.arg(from_date) AND sqlc.arg(to_date)
  AND x.deleted_at IS NULL
  AND x.paid_invoice_id IS NOT NULL
ORDER BY x.dated_on, x.id;


-- name: ListBillPaymentsForVatReturn :many
-- CASH INPUT VAT. BILL_PAYMENT / BILL_REFUND bank explanations (linked to the bill
-- via paid_bill_id) in the period; the engine apportions the bill's VAT to the
-- amount paid. gross_value_minor is SIGNED (− payment out / + refund in), so a
-- refund correctly reduces the reclaim.
SELECT
    x.id,
    x.dated_on,
    x.gross_value_minor,
    b.reference,
    b.comments,
    b.sales_tax_value_minor AS bill_vat_minor,
    b.total_value_minor     AS bill_total_minor
FROM bank_transaction_explanations x
JOIN bills b ON b.id = x.paid_bill_id
WHERE x.organisation_id = sqlc.arg(organisation_id)
  AND x.dated_on BETWEEN sqlc.arg(from_date) AND sqlc.arg(to_date)
  AND x.deleted_at IS NULL
  AND x.paid_bill_id IS NOT NULL
ORDER BY x.dated_on, x.id;


-- =============================================================================
-- vat_returns — the saved snapshot + the filed-period lock.
-- =============================================================================

-- name: UpsertVatReturnFiled :exec
-- Persists the computed return for a period and marks it FILED. One live row per
-- (org, period) — re-filing overwrites the snapshot (ON CONFLICT on the partial
-- unique index). The boxes are passed in already-computed (pence); filed_at is set
-- by the DB. Once written, the period is locked (see IsDateInFiledPeriod).
INSERT INTO vat_returns (
    organisation_id, created_by_user_id, period_start, period_end, period_key, accounting_basis,
    box1_vat_due_sales, box2_vat_due_acquisitions, box3_total_vat_due, box4_vat_reclaimed, box5_net_vat,
    box6_total_sales_ex_vat, box7_total_purchases_ex_vat, box8_ec_dispatches_ex_vat, box9_ec_acquisitions_ex_vat,
    filing_due_on, filing_status, filed_at,
    payment_due_on, payment_amount_due_minor, payment_status
) VALUES (
    sqlc.arg(organisation_id), sqlc.arg(created_by_user_id), sqlc.arg(period_start), sqlc.arg(period_end), sqlc.arg(period_key), sqlc.arg(accounting_basis),
    sqlc.arg(box1), sqlc.arg(box2), sqlc.arg(box3), sqlc.arg(box4), sqlc.arg(box5),
    sqlc.arg(box6), sqlc.arg(box7), sqlc.arg(box8), sqlc.arg(box9),
    sqlc.arg(filing_due_on), 'marked_as_filed', now(),
    sqlc.arg(payment_due_on), sqlc.arg(payment_amount_due_minor), sqlc.arg(payment_status)
)
ON CONFLICT (organisation_id, period_start, period_end) WHERE deleted_at IS NULL
DO UPDATE SET
    created_by_user_id          = EXCLUDED.created_by_user_id,
    period_key                  = EXCLUDED.period_key,
    accounting_basis            = EXCLUDED.accounting_basis,
    box1_vat_due_sales          = EXCLUDED.box1_vat_due_sales,
    box2_vat_due_acquisitions   = EXCLUDED.box2_vat_due_acquisitions,
    box3_total_vat_due          = EXCLUDED.box3_total_vat_due,
    box4_vat_reclaimed          = EXCLUDED.box4_vat_reclaimed,
    box5_net_vat                = EXCLUDED.box5_net_vat,
    box6_total_sales_ex_vat     = EXCLUDED.box6_total_sales_ex_vat,
    box7_total_purchases_ex_vat = EXCLUDED.box7_total_purchases_ex_vat,
    box8_ec_dispatches_ex_vat   = EXCLUDED.box8_ec_dispatches_ex_vat,
    box9_ec_acquisitions_ex_vat = EXCLUDED.box9_ec_acquisitions_ex_vat,
    filing_due_on               = EXCLUDED.filing_due_on,
    filing_status               = 'marked_as_filed',
    filed_at                    = now(),
    payment_due_on              = EXCLUDED.payment_due_on,
    payment_amount_due_minor    = EXCLUDED.payment_amount_due_minor,
    payment_status              = EXCLUDED.payment_status,
    updated_at                  = now();


-- name: UpsertVatReturnHmrcFiled :exec
-- Persists the computed return when submitted online to HMRC. Like
-- UpsertVatReturnFiled but sets filing_status = 'filed' and also stores
-- the HMRC response fields (processing date, form bundle number, charge ref).
-- One live row per (org, period) — re-submission overwrites (ON CONFLICT).
INSERT INTO vat_returns (
    organisation_id, created_by_user_id, period_start, period_end, period_key, accounting_basis,
    box1_vat_due_sales, box2_vat_due_acquisitions, box3_total_vat_due, box4_vat_reclaimed, box5_net_vat,
    box6_total_sales_ex_vat, box7_total_purchases_ex_vat, box8_ec_dispatches_ex_vat, box9_ec_acquisitions_ex_vat,
    filing_due_on, filing_status, filed_at, filed_reference,
    payment_due_on, payment_amount_due_minor, payment_status,
    hmrc_processing_date, hmrc_charge_ref, finalised
) VALUES (
    sqlc.arg(organisation_id), sqlc.arg(created_by_user_id), sqlc.arg(period_start), sqlc.arg(period_end), sqlc.arg(period_key), sqlc.arg(accounting_basis),
    sqlc.arg(box1), sqlc.arg(box2), sqlc.arg(box3), sqlc.arg(box4), sqlc.arg(box5),
    sqlc.arg(box6), sqlc.arg(box7), sqlc.arg(box8), sqlc.arg(box9),
    sqlc.arg(filing_due_on), 'filed', sqlc.arg(processing_date), sqlc.arg(form_bundle_number),
    sqlc.arg(payment_due_on), sqlc.arg(payment_amount_due_minor), sqlc.arg(payment_status),
    sqlc.arg(processing_date), sqlc.arg(charge_ref_number), true
)
ON CONFLICT (organisation_id, period_start, period_end) WHERE deleted_at IS NULL
DO UPDATE SET
    created_by_user_id          = EXCLUDED.created_by_user_id,
    period_key                  = EXCLUDED.period_key,
    accounting_basis            = EXCLUDED.accounting_basis,
    box1_vat_due_sales          = EXCLUDED.box1_vat_due_sales,
    box2_vat_due_acquisitions   = EXCLUDED.box2_vat_due_acquisitions,
    box3_total_vat_due          = EXCLUDED.box3_total_vat_due,
    box4_vat_reclaimed          = EXCLUDED.box4_vat_reclaimed,
    box5_net_vat                = EXCLUDED.box5_net_vat,
    box6_total_sales_ex_vat     = EXCLUDED.box6_total_sales_ex_vat,
    box7_total_purchases_ex_vat = EXCLUDED.box7_total_purchases_ex_vat,
    box8_ec_dispatches_ex_vat   = EXCLUDED.box8_ec_dispatches_ex_vat,
    box9_ec_acquisitions_ex_vat = EXCLUDED.box9_ec_acquisitions_ex_vat,
    filing_due_on               = EXCLUDED.filing_due_on,
    filing_status               = 'filed',
    filed_at                    = EXCLUDED.filed_at,
    filed_reference             = EXCLUDED.filed_reference,
    payment_due_on              = EXCLUDED.payment_due_on,
    payment_amount_due_minor    = EXCLUDED.payment_amount_due_minor,
    payment_status              = EXCLUDED.payment_status,
    hmrc_processing_date        = EXCLUDED.hmrc_processing_date,
    hmrc_charge_ref             = EXCLUDED.hmrc_charge_ref,
    finalised                   = true,
    updated_at                  = now();


-- name: IsDateInFiledPeriod :one
-- The lock check: TRUE when `dated_on` falls inside a SUBMITTED (marked_as_filed /
-- filed / pending) return for the org. The source domains call this in their
-- update/delete to refuse changing a record in a filed VAT period.
SELECT EXISTS (
    SELECT 1 FROM vat_returns
    WHERE organisation_id = sqlc.arg(organisation_id)
      AND deleted_at IS NULL
      AND filing_status IN ('marked_as_filed','filed','pending')
      AND sqlc.arg(dated_on)::date BETWEEN period_start AND period_end
) AS locked;


-- name: GetVatReturnByPeriod :one
-- The saved snapshot + filing/payment status for one period (NULL row if never
-- filed). For an UNFILED period this just supplies the display status over a live
-- recompute; for a FILED period (filing_status in the submitted set) the boxes here
-- are AUTHORITATIVE — the return is rendered from this frozen snapshot, never a live
-- recompute, so a filed return can't drift from what was actually filed. accounting_basis
-- is the basis used at filing time (so the Full-Report lines are recomputed on it).
SELECT
    filing_status,
    filed_at,
    filed_reference,
    payment_status,
    accounting_basis,
    box1_vat_due_sales          AS box1,
    box2_vat_due_acquisitions   AS box2,
    box3_total_vat_due          AS box3,
    box4_vat_reclaimed          AS box4,
    box5_net_vat                AS box5,
    box6_total_sales_ex_vat     AS box6,
    box7_total_purchases_ex_vat AS box7,
    box8_ec_dispatches_ex_vat   AS box8,
    box9_ec_acquisitions_ex_vat AS box9
FROM vat_returns
WHERE organisation_id = sqlc.arg(organisation_id)
  AND period_start    = sqlc.arg(period_start)
  AND period_end      = sqlc.arg(period_end)
  AND deleted_at IS NULL;


-- name: ListVatReturnSummaries :many
-- The (period, filing_status) of every saved return for the org — the period list
-- joins these in to show each period's real status.
SELECT period_end, filing_status
FROM vat_returns
WHERE organisation_id = sqlc.arg(organisation_id)
  AND deleted_at IS NULL;
