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
