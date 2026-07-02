# Backlog / Deferred Items

A running list of work that was intentionally deferred, plus notable TODOs found
in the code. Add to this file whenever you defer something so it isn't lost in a
commit message or chat. Remove items as they're completed.

_Last updated: 2026-07-02_

## Tala (AI assistant)

The in-app AI accountant (`internal/tala`, `POST /api/v1/tala/chat`,
`web/src/views/TalaChatView.vue`) shipped as a read + guarded-write assistant.
Deferred:

- **Stream responses (SSE).** The chat is request/response — the turn is a single
  JSON reply. Stream tokens (and tool-call status) over SSE for a live-typing UX
  and to avoid long waits on hard questions. _Files: `internal/tala/service.go`
  (a streaming `RunTurn`), `handler.go`, `web/src/views/TalaChatView.vue`._
- **Persisted chat threads + history.** The conversation is stateless (the SPA
  holds the history and re-sends it each turn). Persist threads per user/org for
  continuity across reloads and an audit trail. _New table + `internal/tala`._
- **In-loop write execution (human-in-the-loop).** Guarded writes are propose →
  confirm through the existing endpoints; the agent never mutates. A future mode
  could execute a confirmed action inside the loop (pause/resume) so Tala can chain
  multi-step changes. _Files: `internal/tala/propose.go`, `service.go`._
- **More propose tools + domains.** v1 proposes create/approve expense only, and
  reads expenses/invoices/bills/banking/VAT/reports/overview. Add contacts/projects/
  payroll reads and invoice/bill create + more actions. _Files:
  `internal/tala/tools.go`, `propose.go`._
- **Cost & abuse controls.** Per-org rate limiting, a 1h prompt-cache TTL, and
  model tiering via `TALA_MODEL` for cost. _Files: `internal/tala`._

## Auth & authorization

- **God-view: impersonation / act-as another org.** The platform superuser
  (`users.is_superuser`, `internal/platformadmin`) is deliberately READ-ONLY — it
  browses all orgs/users but cannot enter an org and act. A future "assume org"
  mode would re-mint a token scoped to any org (not just the superuser's
  memberships), likely with an audit trail. _Files: `internal/platformadmin`,
  `internal/userauth` (switch endpoint could be generalised)._

- **God-view: pagination on the admin lists.** `ListAllOrganisations` /
  `ListAllUsers` return every row unbounded (fine at dev scale). Add LIMIT/OFFSET
  (or keyset) + a search box before the platform grows. _Files:
  `db/queries/auth.sql`, `internal/platformadmin`, `web/src/views/admin`._

- **Widen org-wide read beyond owner/admin.** `isOrgAdmin` currently grants
  read-all to `owner` + `admin` only. The schema documents `accountant` and
  `read_only` as read-all financial roles — decide whether they should also see
  all of an organisation's expenses. _File: `expense_service.go` (`isOrgAdmin`)._

- **Failed-login tracking & lockout.** The login handler authenticates but does
  not record attempts. The sqlc methods already exist
  (`RecordSuccessfulLogin`, `IncrementFailedLogin`, `LockUser`) — wire them in
  (`RecordSuccessfulLogin` needs the client IP as `*netip.Addr`) and have the
  login flow respect `locked_until`. _Files: `auth_handler.go`, `db/auth`._

- **Login response: token expiry / refresh.** Login returns `access_token` +
  `user` only. Consider returning the access-token expiry and adding a
  refresh-token flow (access tokens are short-lived — default 15m). If a refresh
  token is delivered via an httpOnly cookie, also flip CORS `AllowCredentials` to
  `true` in `server.go` and keep explicit origins (the wildcard `*` is then
  forbidden). _Files: `auth_handler.go`, `server.go`._

- **Organisation switching.** The token is scoped to the user's first active
  organisation at login. Add a "switch organisation" endpoint that re-mints a
  token for another org the user belongs to (use `ListOrganisationsForUser`).
  _File: `auth_handler.go`._

- **Email verification / account activation.** Reuse the password-reset plumbing
  — `generateToken`/`hashToken` (`auth_handler.go`) + the `EmailSender` — with the
  schema's existing `email_verification_token` columns and `SetEmailVerificationToken`
  / `GetUserByVerificationToken` / `VerifyUserEmail` queries (a SEPARATE token type;
  confirm marks the email verified / activates rather than setting a password).
  Also needs a registration/sign-up endpoint, which doesn't exist yet.
  _Files: `auth_handler.go`, `email_content.go`, `db/auth`._

- **Logged-in change-password endpoint.** Let an authenticated user change their
  own password — authenticated via the `Authorization` header (NOT a token in the
  URL) and requiring the current password (bcrypt-verify) before setting the new
  one. Reuses `UpdateUserPassword`. _File: `auth_handler.go`._

- **Rate-limit forgot-password.** `POST /auth/forgot-password` is currently
  unthrottled — add per-email / per-IP rate limiting to curb reset-email flooding.
  _File: `auth_handler.go`._

- **HTML password-reset email.** The reset email is plain text
  (`email_content.go`); add an HTML/multipart version for nicer rendering in mail
  clients. _Files: `email_content.go`, `email_smtp.go`._

### Users / members management (the admin "Users" screen)

The owner/admin Users list (`/users`) + the unified User Details edit
(`GET`/`PUT /api/v1/members/:id`, `internal/members`) shipped, but deliberately
scoped. Deferred:

- **New User — email-invite variant.** The "New user" button + add-user form now
  ship (`POST /api/v1/members`, `internal/members.CreateMember`): an owner/admin
  sets an initial password, creating an ACTIVE user who can log in immediately. The
  alternative **email-invite** flow is still deferred — a create/invite endpoint that
  sends an email and creates a `pending` membership instead of taking a password.
  The plumbing exists (`CreateInvitedMembership` + `AcceptInvite`, `db/queries/auth.sql`,
  and the `EmailSender` seam) but there is no sign-up/accept endpoint yet (overlaps
  with the email-verification item above). Also deferred: adding a *pre-existing*
  user to a second org (the current flow returns 409 on a duplicate email).
  _Files: `internal/members`, `internal/userauth`, `db/auth`._

- **Payroll "position" vs access role.** The FreeAgent screenshot's Role dropdown
  is a payroll position (Director/Employee), distinct from our access-control role
  (owner/admin/member/…). The Users screen currently edits the access role; if the
  payroll module needs an explicit employment position, add it as a separate field
  (likely a new column) rather than overloading the membership role.

- **2FA.** The Users list's "2FA authenticator app" column is a static "Disabled"
  placeholder — there is no 2FA feature. Implement TOTP enrolment/verification and
  surface real status. _Files: `internal/userauth`, `db/auth` (new columns)._

- **NI/UTR/DOB are validated, not DB-constrained.** Format is enforced in
  `internal/kernel/payroll.go` (NINO shape, 10-digit UTR, DOB range); the columns
  are plain nullable `VARCHAR`/`DATE`. If stricter guarantees are wanted later,
  add DB `CHECK`s — but keep imports/partial entry in mind.

- **Payroll conditional detail (the deactivated "Yes" paths).** The admin payroll
  sections (`employee_payroll` table; nested on `/api/v1/members/:id`) capture the
  top-level selectors, but two affirmative options are rendered **disabled** in the
  UI because their detail isn't built yet:
  - **Statutory Pay = Yes** → enter statutory payment amounts (paternity/maternity/etc.).
  - **Pension = "Yes, making contributions"** → employer/employee contribution amounts
    + pension scheme.
  The DB columns/enums already allow these values (forward-compatible). Re-enable the
  options once the amount entry exists. _Files: `db/schema/auth_schema.sql`,
  `internal/members/payroll.go`, `web/src/views/MyDetailsView.vue`._

- **Payroll "existing employee" onboarding detail.** "Is this employee already on your
  payroll? = Yes" should capture year-to-date figures / pay-in-previous-employment for a
  mid-year start; only the flag is stored today. Also: student-loan **plan types** and the
  NI-letter-driven calculations belong to the actual pay-run engine, not this data-capture
  form.

- **List filtering & pagination for `GET /api/v1/expenses`.** Today the endpoint returns the full set the caller may see (owner/admin: whole org; others: own) with no filters or paging. Add query-param filtering (date range, status, project) and pagination (limit/offset or cursor), preserving the existing owner/admin-vs-own scoping in `ListExpenses`. Reuse the existing org-scoped sqlc queries `ListExpensesByDateRange` / `ListExpensesByStatus` / `ListExpensesByProject` (`db/queries/query.sql`); user-scoped + filtered variants would need new queries. _Files: `expense_service.go`, `server.go`, `db/queries/query.sql`._

### Payroll pay-run engine (`internal/payroll`)

The pay-run / payslip engine + simplified PAYE/NI calculator shipped (`internal/payroll`,
`db/payroll`, `db/schema/payroll_schema.sql`, the four global rate tables seeded by
`db/seeds/payroll_rates_2026_27.sql`). Deliberately scoped; deferred:

- **HMRC RTI / FPS / EPS filing.** A completed run reads as "report unfiled" — there is
  no Real Time Information submission. Add the FPS/EPS XML + Government Gateway transport
  (mirrors the VAT MTD integration). `pay_runs.status` only goes `draft → completed`.
- **Statutory pay calculation.** SSP/SMP/SPP/etc. are stored + editable on the payslip but
  taken AS ENTERED — not auto-calculated from absence/average-earnings rules.
- **Pension contributions.** `employee_pension_minor` / `employer_pension_minor` are always
  0 (auto-enrolment %, qualifying earnings, scheme reference all deferred — ties into the
  profile's deferred "making contributions" detail).
- **Student-loan deductions.** The undergraduate/postgraduate flags are snapshotted but
  `student_loan_minor` is always 0 (plan-type thresholds not implemented).
- **NI category rates beyond the confirmed set.** Only A is confirmed against reference
  data; C/J/H/M/Z are seeded with best-known values and B is omitted. Verify every letter
  against the gov.uk per-category table; the engine REJECTS an unseeded letter rather than
  guessing. Also: H/M/Z employer 0% ignores the Upper Secondary Threshold (their relief
  actually stops at the UST — model the UST to be correct above ~£50k).
- **K-codes (negative free pay)** and the directors' "alternative arrangements" final-month
  recalculation (treated as ordinary monthly today).
- **Weekly / other frequencies.** `pay_runs.frequency` is constrained to `monthly`; the
  period arithmetic in `internal/payroll/periods.go` is monthly-only.
- **Employee self-service payslip view.** Payroll is owner/admin only; employees can't view
  their own payslips yet (no per-user read path).
- **2026/27 rate verification.** Confirm `db/seeds/payroll_rates_2026_27.sql` against gov.uk
  before production reliance (thresholds were legislated frozen, so they should carry over).
- ~~**Per-employee N+1 in prepare / get-run / overview.**~~ DONE. `PreparePayRun` now fetches
  prior YTD in one grouped query (`ListYearToDateByUserUpToPeriod`), computes in-memory, and
  bulk-inserts all payslips in a single pipelined batch (`CreatePayslipComputed :batchexec`);
  `buildPayRunDetail` resolves names + YTD via two grouped queries (`ListRunPayslipNames` + the
  YTD map) instead of per-payslip lookups. On the 264-member dev org: prepare 2m20s→4.5s,
  get-run 57s→1.4s, overview 29s→1s. _Files: `internal/payroll/service.go`, `db/queries/payroll.sql`._


## Expenses & categories

- **List filtering & pagination for `GET /api/v1/expenses`.** Today the endpoint
  returns the full set the caller may see (owner/admin: whole org; others: own)
  with no filters or paging. Add query-param filtering (date range, status,
  project) and pagination (limit/offset or cursor), preserving the
  owner/admin-vs-own scoping in `ListExpenses`. Reuse the existing org-scoped
  sqlc queries `ListExpensesByDateRange` / `ListExpensesByStatus` /
  `ListExpensesByProject` (`db/queries/query.sql`); user-scoped + filtered
  variants would need new queries. _Files: `expense_service.go`, `server.go`,
  `db/queries/query.sql`._
- **Suggest-category endpoint for manual entry.** The supplier→category
  dictionary (`supplier_category_map`, populated by the `learn_supplier_category()`
  trigger) is currently consumed only on the Smart Upload / OCR path
  (`OcrService.suggestCategory`). For the manual "new expense" form, add a small
  read endpoint (e.g. `GET /api/v1/expenses/suggest-category?supplier=…`) over the
  existing `GetSuggestedCategory` query so the SPA can pre-select the category as
  the user types the supplier. Org-scoped; read-only. _Files: `server.go`,
  `expense_service.go`._
- **Validate the project link on expense create/update (multi-tenant hardening).**
  `CreateExpense`/`UpdateExpense` accept `project_id` (now sent by the activated
  "Is this a project expense?" card) but don't verify it belongs to the caller's
  organisation — a crafted request could link an expense to another org's project.
  Add an org-scoped check (inject the projects querier into `ExpenseService` and look
  the project up by `(id, organisation_id)`, mirroring the claimant `GetMembership`
  check). _Files: `expense_service.go`, `main.go`._
- **Expense rebilling (project expenses).** The `expenses` table has `rebill_type`
  (`cost|markup|price`) + `rebill_factor`, and the detail view already renders them,
  but the form only links a project (no rebilling UI) and the backend does no
  validation. Add rebilling controls to the project card plus service validation
  (rebill_type enum, the all-three-together combination → 422 not a DB 500), and
  decide the markup representation (store a multiplier vs a percentage). _Files:
  `web/src/views/ExpenseEntryView.vue`, `expense_service.go`, `server.go`._
- **Project name in the expense detail response.** `ExpenseDetailView` resolves the
  linked project's name with a second `GET /projects/:id` call. To drop the extra
  round-trip, LEFT JOIN `projects` in the `v_expenses_full` view and expose
  `project_name` on `ExpenseDetailResponse` (like `category_name`). _Files:
  `db/schema/schema.sql`, `server.go`, `expense_service.go`._
- **CSV import processing (`POST /api/v1/expenses/import`).** The Import dialog on
  the expenses list currently only downloads the template + field guide
  (`web/src/views/ExpenseListView.vue`); the actual upload/parse is deferred. Add a
  multipart endpoint that parses the uploaded CSV (the 12-column lean template — see
  the export's `expenseExportHeader`), validates each row, resolves `category` (name
  **or** nominal code) and `claimant_email`, parses the `DD/MM/YYYY` date and the
  `sales_tax_rate` percent → a VAT rate, then creates **DRAFT** expenses via the
  existing `CreateExpense`. Return a per-row result (created count + row-numbered
  errors) for partial success; add file-size / row-count caps and a dedupe strategy.
  Then enable the dialog's upload control. _Files: `server.go`, `expense_service.go`,
  `attachment_handler.go` (multipart pattern), `web/src/views/ExpenseListView.vue`,
  `web/src/services/expenses.service.ts`._
- **Widen the import/export template beyond the lean set.** The CSV template ships
  the 12 columns we support today; FreeAgent's reference template also has `type`,
  `project_client` / `project_name`, `rebill_by` / `rebill_factor_amount`,
  `stock_item_name` / `stock_quantity`, `asset_life`, and the native-currency
  `native_gross_value` / `native_sales_tax_value`. Add these columns (and the
  export/import handling) as the underlying features land (project rebilling, stock,
  capital assets, multi-currency). _Files: `expense_service.go` (`expenseExportHeader`,
  `ExpenseExportRow`), `web/public/expense_import_template.csv`,
  `web/src/views/ExpenseListView.vue` (`templateFields`)._
- **Export niceties.** The export shares the import template's exact columns for a
  clean round-trip, so it omits read-only context like `status` and `created_at` —
  add them as extra trailing columns if users want them (import would ignore them).
  Also revisit the CSV-injection guard (`sanitizeCSVField` prefixes a `'` to
  free-text cells starting with `= + - @`): it alters the value, so the future import
  should strip a single leading quote to round-trip cleanly. _File:
  `expense_service.go`._

## Contacts

- **Require a name or an organisation name.** The contacts table is deliberately
  permissive — `first_name`, `last_name` and `organisation_name` are all nullable
  with no cross-column CHECK (so a contact known only by email isn't rejected by
  the DB). Add the FreeAgent-style rule "a contact needs a first+last name AND/OR
  an organisation name" as an app-layer check in `ContactService.CreateContact` /
  `UpdateContact` (return `ErrValidation`). _File: `contact_service.go`._
- **Contact type (customer vs supplier) + active/archive.** The table has
  `is_active`, but the CRUD endpoints don't expose archiving, and there's no
  customer/supplier classification yet (the New Contact form had none). Add when
  invoices/bills need to filter contacts by role. _Files:
  `db/schema/contacts_schema.sql`, `db/queries/contacts.sql`, `contact_service.go`._
- **Attribute an expense to a supplier contact.** Invoices now reference contacts
  (`invoices.contact_id`, datalayer landed 2026-06-23) and projects already did
  (`projects.contact_id`). The remaining gap is the EXPENSES side: add a nullable
  `contact_id` FK on `expenses` + the lookup so a receipt can be attributed to a
  supplier. _Files: `db/schema/schema.sql`, `expense_service.go`._
- **List filtering, search & pagination for `GET /api/v1/contacts`.** Returns the
  whole org's contacts, ordered by name only, unpaged. Add name/email search, an
  active-only filter, and pagination. _Files: `contact_service.go`, `server.go`,
  `db/queries/contacts.sql`._

## Invoices

The DATALAYER + BACKEND landed (2026-06-23). Datalayer: `db/schema/invoices_schema.sql`
(`invoices` + `invoice_items`), `db/queries/invoices.sql`, the generated `db/invoices` package.
Backend: `internal/invoices` (`Service` + `Handler` + DTOs + `status.go`), self-registering
`/api/v1/invoices` (CRUD + `POST /:id/status`), wired in `main.go`; the add-on VAT helpers
(`money.AddOnVAT` / `money.PercentToBps` / `money.BpsToPercentString`); and the derived display
status. Tested in `invoice_service_test.go` (real Postgres) + `money/money_test.go`. Still a
MINIMAL cut. Deferred:

- **Frontend SPA — core landed (2026-06-23); refinements deferred.** Built: the invoice list, the
  create/edit header form (contact picker + reference + date + payment-terms→due_on + currency), the
  document-style detail view (Draft→Sent→Paid tracker, line-item table + totals, Payment Details /
  Other Information), the "New invoice item" modal, and the issue/reopen (Mark as sent / Back to
  draft) actions. _Files: `web/src/views/Invoice{List,Entry,Detail}View.vue`,
  `web/src/components/InvoiceItemDialog.vue`, `web/src/services/invoices.service.ts`,
  `web/src/types/invoice.ts`, `web/src/lib/invoiceStatus.ts`, router + `AppTopBar`._ Deferred UI: the
  FreeAgent fields with no backend column (line **Units**/`item_type`, "**add to price list**",
  invoice **Additional text**/comments, **project** link); the **schedule / write-off / refund**
  status buttons (the backend already supports them); and the **Paid** tracker step (needs the
  payments feature).
- **Per-PROJECT invoice sequence.** Org-level auto-numbering landed (2026-06-24): a
  `next_invoice_number` counter floor on `organisations` + `GET /api/v1/invoices/next-reference`
  (the zero-padded next number, pre-filled on the create form). The suggestion is **self-healing** —
  `GREATEST(counter floor, highest numeric reference in use + 1)` — so it can't get stuck behind the
  references actually in use; on create a numeric reference raises the floor (monotonic `GREATEST`,
  so numbers are never reused). `reference` is now REQUIRED + unique per org. Still deferred:
  honouring `projects.project_invoice_sequence` so a project can use its OWN number sequence instead
  of the org-wide one. _Files: `internal/invoices`, the projects domain._
- **Payments / reconciliation — `paid_value_minor` now wired from `INVOICE_RECEIPT`** (2026-06-24).
  Explaining a money-in bank line as an Invoice Receipt sets `bank_transaction_explanations.paid_invoice_id`
  and re-syncs `invoices.paid_value_minor = Σ(live receipts)` in the same transaction (so a SENT invoice
  now derives Paid/partial); `GET /api/v1/invoices/outstanding` backs the picker; overpayment is capped
  at the outstanding balance; and a SENT invoice with any payment **cannot be reopened** (409 — the
  receipt(s) must be removed first, which keeps a DRAFT's paid at 0 and so editing safe). Still deferred:
  a dedicated payments table; `paid_on` / `written_off_date` columns (and splitting the single
  `UpdateInvoiceStatus` into per-transition queries that stamp them). _Files: schema,
  `db/queries/invoices.sql`, `internal/banking`, `internal/invoices`._
- **Wire `ContactHasInvoices` into the contacts in-use guard.** The datalayer query exists but is
  unused; inject the invoices querier into `contacts.NewService` and OR it into the existing
  `ContactHasProjects` check, so a contact with invoices can't be soft-deleted and reports
  `in_use=true`. _Files: `internal/contacts/service.go`, `main.go`._
- **Tighten status-change authz (optional).** Create / edit / delete / status all use the contacts
  rule (creator or owner/admin). If issuing / writing-off / refunding should be owner/admin-only,
  gate `ChangeStatus` on `kernel.IsOrgAdmin`. _File: `internal/invoices/service.go`._
- **Service-layer currency validation.** `currency` is upper-cased + defaulted to GBP, but only the
  DB FK rejects an unknown code (a 500-class). Validate against `currencies` for a clean 422 (the
  same deferred item as expenses/projects). _File: `internal/invoices/service.go`._
- **Restore the deferred FreeAgent surface** as features land: header `project_id`,
  `discount_percent`, `comments`, `po_reference`, `ec_status`, `place_of_supply`, display flags
  (`omit_header`, `show_project_name`); multi-currency (`exchange_rate` + native-currency totals);
  line `item_type`, `sales_tax_status`, second sales tax, per-line `category_id` (income CoA →
  `categories`) / `project_id`; CIS (`cis_rate` / `cis_deduction*`); payment methods / online
  payment URL; `send_*_emails`; `recurring_invoice`; `include_timeslips/expenses/estimates`;
  `property` / `bank_account` links. _File: `db/schema/invoices_schema.sql` (+ queries)._
- **Invoice audit log.** No history table yet (same gap as the unwired `expense_audit_log`).
- **Turn `expenses.rebilled_invoice_id` into a real FK** to `invoices(id)` now that the table
  exists (it's a bare UUID today). _File: `db/schema/schema.sql`._

## Bills (accounts payable)

The DATALAYER + BACKEND landed (2026-06-24). Datalayer: `db/schema/bills_schema.sql` (`bills` +
`bill_attachments`), `db/queries/bills.sql`, the generated `db/bills` package. Backend: `internal/bills`
(`Service` + `Handler` + DTOs), self-registering `/api/v1/bills` (CRUD) + `/api/v1/bill-categories`,
wired in `main.go`. A bill is the payable twin of an invoice, modelled on the FreeAgent New Bill screen
— a SINGLE flat spending line (like an expense): supplier `contact_id`, supplier `reference` (no
auto-numbering), `dated_on` / `due_on`, `category_id` → CoA `categories` (picker filtered to spending
accounts), BIGINT-pence net/sales_tax/total/paid + generated `due_value_minor`, `comments`,
`is_hire_purchase`, optional `project_id`. VAT follows the EXPENSES pattern (`vat_rate_id` +
`is_fixed_ratio` extract via the new `money.ExtractVAT`, or a manual `vat_amount`); there is NO status
lifecycle (a bill is editable/deletable only while unpaid) and `paid_value_minor` is the banking
module's to write. Tested in `bill_service_test.go` + `money/money_test.go` (`ExtractVAT`). Deferred:

- **SPA bill form** (`web/`) — list + create/edit + the supplier / spending-category / VAT-rate pickers
  (reuses the existing `GET /api/v1/vat-rates` and the new `GET /api/v1/bill-categories`). Not built yet.
- **Bill attachments service** (GCS upload + Smart Capture / OCR). `bill_attachments.ocr_*` columns +
  queries exist, but the upload + Document AI + skeleton-draft pipeline isn't wired (mirrors the
  expenses capture path). _Files: an `internal/attachments`-style bill service, `internal/ocr`, `internal/storage`._
- **Banking writes `paid_value_minor` — LANDED (2026-06-25).** Explaining a money-out bank line as a
  **Bill Payment** (`BILL_PAYMENT`, entity_link `BILL`) links it via `bank_transaction_explanations.paid_bill_id`
  and re-syncs `bills.paid_value_minor = Σ(-live BILL_PAYMENT grosses)` (negated, since money-out grosses
  are negative) in the explanation's transaction — the money-out mirror of the Invoice Receipt flow.
  `GET /api/v1/bills/outstanding` backs the picker; overpayment is rejected; a paid bill locks. Tested in
  `banking_service_test.go` (`TestBillPaymentExplain`) + `bill_service_test.go` (`TestListOutstandingBills`).
  Still deferred: a `paid_on` column (the date the payment cleared), and the `BILL_PAYMENT_REFUND` reverse.
- **Wire `ContactHasBills` into the contacts in-use guard.** The datalayer query exists but is unused;
  inject the bills querier into `contacts.NewService` and OR it into the existing
  `ContactHasProjects` / `ContactHasInvoices` check so a contact with bills can't be soft-deleted and
  reports `in_use=true`. _Files: `internal/contacts/service.go`, `main.go`._
- **Including/Excluding-VAT entry mode.** Dropped in favour of expenses-style inclusive-only entry. If a
  net-entry mode is wanted, re-add an `amounts_include_vat` flag + use `money.AddOnVAT`. _Files:
  `db/schema/bills_schema.sql`, `internal/bills/service.go`._
- **`reference` uniqueness.** Not enforced (a supplier number isn't ours). If desired, add a partial
  unique index on `(organisation_id, contact_id, reference)`. _File: `db/schema/bills_schema.sql`._
- **Multi-line bills** (`bill_items` child table, like `invoice_items`) for bills that split across
  several spending categories; **supplier→category learning** for bills (a `learn_supplier_category()`
  analogue); **hire-purchase agreement** entity + instalment schedule behind `is_hire_purchase`;
  and **multi-currency** (`exchange_rate` + native-currency totals). _File: `db/schema/bills_schema.sql`._
- **Bill audit log.** No history table yet (same gap as the unwired `expense_audit_log`).

## Organisation / Company details

- **Assign an owner / members to a superuser-created org.** The god-view "Create
  Organisation" flow (`internal/platformadmin.CreateOrganisation` +
  `POST /api/v1/admin/organisations`) makes an **empty** org (no members) plus its
  chart of accounts. Nobody can log into it until a membership exists — add a
  god-view "add/assign user to org" action (create `owner` membership, optionally
  create the user). _Files: `internal/platformadmin`, `db/queries/auth.sql`
  (`CreateMembership`), `web/src/views/admin/`._
- **Set company_type / timezone / slug at org creation.** The create form collects
  only name + country + currency; `company_type` and address are set afterward on
  Company Details, `timezone` defaults to `Europe/London`, and `slug` is
  auto-derived from the name (kebab-case, name clash → 409) and not user-editable.
  Consider a timezone picker + an editable/validated slug. _Files:
  `internal/platformadmin` (`slugify`, `CreateOrganisation`), `db/queries/auth.sql`._
- **Self-service signup.** There is still no public registration flow; orgs are
  created only by a superuser (above) or seeds. A real signup would create the
  founding user + org + owner membership + CoA in one flow. Country + native
  currency are chosen there; changing them afterwards stays disallowed (would
  require re-denominating stored money). _Files: future signup handler,
  `db/queries/auth.sql`, `internal/organisation`._
- **Backfill + drop `registered_address`.** The structured address columns
  (`address_line_1..3`, `town`, `region`, `postcode`) supersede the legacy
  free-text `registered_address`, which is no longer written but kept for
  back-compat. Backfill any existing data into the structured columns, then drop
  the column in a later additive migration. _File: `db/schema/auth_schema.sql`._
- **`business_category` dropdown + controlled list.** The Company Details screen
  renders `business_category` as a free-text input
  (`web/src/views/CompanyDetailsView.vue`), but the FreeAgent screen it mirrors
  shows a fixed dropdown (e.g. "Marketing & Advertising"). Add a curated category
  list (a `web/src/lib/businessCategories.ts` const + a `Select`, mirroring
  `lib/countries.ts`); the column stays a free VARCHAR until the list firms up,
  then promote it to a DB enum / reference table with a CHECK or FK. _Files:
  `web/src/views/CompanyDetailsView.vue`, `db/schema/auth_schema.sql`,
  `organisation_service.go`._
- **Enforce `company_type` "set once".** The form notes that changing company type
  requires a fresh account. The column is freely editable today; add a rule
  (app-layer or trigger) that blocks changing it once set, if that policy is wanted.
  _Files: `organisation_service.go`, `db/schema/auth_schema.sql`._
- **Surface VAT on Company Details.** `vrn` exists but isn't on this form yet, so
  the service preserves it via read-modify-write. When VAT is added to the screen,
  add `vrn` (+ any VAT scheme) to `UpdateOrganisationRequest` and validate the
  `GB` + 9/12-digit format. _Files: `organisation_service.go`, `server.go`._
- **Format validation for UK references.** `paye_reference`,
  `accounts_office_reference` and `postcode` are stored as free text. Add format
  checks (PAYE `NNN/XXNNNNN`, Accounts Office `NNNXXNNNNNNNN`, UK postcode) in the
  service. _File: `organisation_service.go`._

## Exchange rates & foreign-currency gain/loss

Full plan: `~/.claude/plans/in-order-to-create-binary-manatee.md` (4 phases).

- **Phase 1 — exchange-rate module + invoice auto-fill — LANDED (2026-06-29).** New
  `exchange_rates` table (global, GBP-relative: HOME per 1 unit of currency) +
  `db/queries/fxrates.sql` → sqlc package `db/exchange_rates`; `internal/fxrates`
  (`Provider` seam + `frankfurterProvider` = ECB, free, no key; `Service`
  RefreshRates/RateOnOrBefore/Lookup/ListOnDate; read `Handler`
  `GET /api/v1/exchange-rates[/:currency]`; OIDC-gated
  `POST /internal/v1/fxrates/refresh`). The OIDC middleware was lifted to
  `kernel.RequireWorkflowOIDC` (integrations now delegates to it). Invoices
  auto-fill a foreign `exchange_rate` from the stored rate for `dated_on`
  (`internal/invoices` `RateLookup` seam) — explicit rate still wins; no rate ⇒ 422.
  Daily refresh runs via Cloud Scheduler (`deploy-fxrates/README.md`) + a best-effort
  startup fetch. Tested in `internal/fxrates/*_test.go` + `invoice_service_test.go`
  (`TestInvoiceForeignCurrencyAutoFillsRate`). Deferred within Phase 1:
  - **SPA auto-fill UI.** `InvoiceEntryView.vue` should call
    `GET /api/v1/exchange-rates/:currency?on=<dated_on>` on currency/date change to
    pre-fill an editable rate field and show the home-currency total beside the
    foreign total. Backend is ready; the Vue piece isn't built. _Files: `web/src/...`._
  - **xe.com provider** (paid; interface ready) / HMRC monthly rates as alt sources.
- **Invoice "Currency Gains/Losses" panel (read-only) — LANDED (2026-06-29).** The
  FreeAgent-style display on a SENT, foreign-currency invoice detail: revalues the
  OUTSTANDING (due) amount at the booking rate vs today's stored rate and shows the
  UNREALISED gain/loss. `internal/invoices` `buildFXSummary` (reuses `s.rates` +
  `money.Apportion`/`ConvertMinor`); served as `fx_summary` on the GET detail only.
  `web/src/views/InvoiceDetailView.vue` renders the card. This is DISPLAY ONLY — it
  posts nothing to the GL (that's Phase 2/3). **Realised is shown as `"0.00"`** (per-
  payment realised needs receipt-date rates → deferred with Phase 2). Tested in
  `invoice_service_test.go` (`TestInvoiceFXSummary`).
- **Phase 2 — realised gain/loss on payment — LANDED (2026-06-29).** Settling a foreign
  invoice with a bank receipt now posts a balanced multi-currency journal that crystallises
  realised FX. `bank_transaction_explanations` gained `currency` / `exchange_rate` /
  `base_value_minor` / `settled_invoice_minor` (the bank-, home- and invoice-currency views
  of the portion, computed at the receipt-date rate). The poster's `ledger.Amount` gained
  optional per-leg `Currency`/`ExchangeRate` so one entry spans three currencies (bank cash,
  invoice-ccy debtor relief, home-ccy FX). `INVOICE_RECEIPT` rule extended to 4 legs:
  DR Bank `GROSS` / CR Debtors `DEBTOR_RELIEF` / CR `FX_REALISED_GAIN` `FX_GAIN` /
  DR `FX_REALISED_LOSS` `FX_LOSS` — both FX roles → the new **390 "Realized Currency
  Exchange Gain/Loss"** (single signed account; gain CR / loss DR). Debtor relief uses the
  invoice's BOOKING rate via the **difference of cumulative apportionments**, so Σ relief =
  `native_total` exactly and the home receivable closes to 0; `repostInvoiceReceipts`
  re-posts ALL of an invoice's receipts (ordered) on each mutation so this stays correct
  under edit/delete/re-point. `resolveInvoice`/`resyncInvoicePaid` now work in invoice-
  currency space (`settled_invoice_minor`). New pure leaf `internal/fx` (`ConvertVia` +
  `RealisedGainLoss`). Tested in `gl_poster_receipt_test.go` (realised loss + residual
  closure) + `internal/fx/fx_test.go`. _Bills realised FX (symmetric CREDITORS path) +
  cross-rate when org home ≠ GBP remain deferred._
- **Phase 3 — periodic unrealised revaluation of open foreign debtors — LANDED (2026-06-30).**
  `internal/fxrevaluation` retranslates each org's OPEN foreign invoices (SENT, due>0,
  currency≠home) to today's stored rate, posting the swing on the due portion to the new
  **391 "Unrealized Currency Exchange Gain/Loss"** via the `INVOICE_REVALUATION` event
  (sign-split `FX_GAIN`/`FX_LOSS` legs → DR/CR 681 vs `FX_UNREALISED_*`→391; new roles +
  CHECK + seed). Cumulative-supersede (poster delete-then-insert) so reruns replace, never
  double. `RunRevaluation(asOf)` is **chained onto the daily FX-rate refresh**
  (`fxrates` `InternalRefresh` → `SetRevaluer`, best-effort). Receipts keep it in step in
  the same tx (`bankingSvc.SetInvoiceRevaluer` → `OnInvoiceReceiptChanged`): a **partial**
  receipt re-revalues the reduced due; a **full settlement** crystallises with an **explicit
  reversing journal** (new `ledger.Poster.ReverseEntry`, `is_reversal=TRUE`) that zeroes 391
  by its OWN balance — realised stays independently in 390 (no double-count). Reopen/write-off
  removes the entry (undo). The Trial Balance + Account Transactions reflect it with no FE
  change. Tested in `fxrevaluation_test.go` (gain, replace-not-double, full-settlement reversal
  + 390 independence). _Deferred within Phase 3: org home currency ≠ GBP cross-rate; a manual
  "revalue now" endpoint (only the daily chain + receipt hooks trigger it today); historical
  as-of-date revaluation (one live entry, dated the last run)._
- **Phase 4 — foreign-currency bank-balance revaluation** (750-x vs 390) — NOT STARTED.
  Account-scheme decision pending: invoice realised FX uses 218 + a sibling loss
  account; bank-cash uses a single combined 390 — confirm whether to unify on 390.
- **Expenses multi-currency — LANDED (2026-07-02).** The expense write path now fills the
  dual-currency columns (mirrors invoices `nativeAmounts`): a native-currency expense stores
  `native_*` == transaction with a NULL `exchange_rate`; a foreign expense converts gross +
  VAT to the org's home currency via `money.ConvertMinor`, auto-filling the rate from the
  stored daily rate (`fxrates.RateOnOrBefore`) or taking an explicit `exchange_rate` (422 if
  neither). SPA: the expense form shows an exchange-rate field for a foreign currency (login
  now returns the org's `native_currency`); the detail view already showed native + rate.
  Tested in `server_test.go` `TestExpenseFX`. _Deferred: `expenses.manual_vat_amount_minor`
  (reclaimable-VAT override for foreign expenses where the rate-based calc doesn't apply) is
  still unused; **GL posting of `EXPENSE_APPROVED`** (the rule already exists in
  `gl_posting_rules`) is the next step — this change makes the row postable; minor cleanup:
  `resolveVAT` + `nativeAmounts` each fetch the org (dedupe into one fetch later)._

## Currencies

- **Make money conversion `minor_unit`-aware.** `currencies.minor_unit` is now
  stored (2 for most, 0 for JPY/KRW, 3 for the Gulf dinars), but `money/money.go`
  still assumes 2 dp everywhere (`PoundsToMinor` × 100). Before supporting a
  non-2dp currency end-to-end, thread `minor_unit` through the pounds↔minor
  conversion. _File: `money/money.go`._

- **Optional `is_active` curation flag on currencies.** If the full ISO 4217 list
  proves too long for the picker, add an `is_active BOOLEAN` column so currencies
  can be hidden from dropdowns without deleting rows (keeps the FK target intact).
  _Files: `db/schema/schema.sql`, `db/queries/currencies.sql`._

- **Validate submitted currency codes in the service layer.** The FK now rejects
  an invalid code at the DB (500-class). For a clean 422, validate against
  `GetCurrencyByCode` in the expense/project services when a currency is set.
  _Files: `expense_service.go`, `project_service.go`._

## Banking

The data layer, the bank-ACCOUNT service/API (`internal/banking`, the
`/api/v1/bank-accounts` CRUD), the account list + entry/edit views, and the
read-only TRANSACTION (statement) view, manual transaction entry (add/edit/delete
views + the "More ▾" menu with delete-account), and CSV statement import
(`POST /:id/transactions/import` + `BankStatementImportView`) have landed. What
remains is the reconciliation/feed richness:

- **Transaction-view richness — period filter + search LANDED; rest remain.** The
  statement now has a **period filter** (months grouped by calendar year + the UK-tax-year
  "Accounting Period" + "All time"; brought-forward recomputed at the period start) and a
  **search** across the description/bank_memo AND the line's explanations (via an
  `explanation_summary` digest on the statement payload) — both client-side over the
  fully-loaded list. Still to do:
  - **"Latest Statement Upload" period option** — needs an upload-batch concept (the CSV
    importer doesn't tag a batch id on the rows it inserts). _Files:
    `internal/banking/statement_import.go`, `db/schema/banking_schema.sql`._
  - **Real accounting-period** (vs the UK-tax-year assumption) — a financial-year-start
    field on the organisation. _File: `db/schema/auth_schema.sql`._
  - **Server-side pagination** — a **client-side pager (25/50/100)** now slices the filtered
    list (mirrors the expenses list), but the statement still LOADS every row; server-side
    paging (limit/offset + a server-computed brought-forward) is the real scale fix. Plus
    **bulk-checkbox actions** and the **account-switcher dropdown**.
- **Bank feed ingestion (TrueLayer / Open Banking).** Populate transactions with
  `source = 'feed'`, deduping on `external_id` via the existing partial unique
  index + `GetBankTransactionByExternalID`. This is the planned FCA Open Banking
  integration. _Files: new ingestion code, `db/queries/banking.sql`._
- **Statement upload — OFX hardening (OFX import DONE).** OFX import landed: a
  hand-rolled, stdlib-only parser (no dependency) in `internal/banking/statement_import.go`,
  auto-detected alongside CSV on the same endpoint, `source = 'statement'`, deduping on
  the bank's FITID via `external_id = "ofx:"+FITID` (the existing per-account unique
  index). Remaining niceties: **QFX** (Quicken's OFX dialect) should work through the
  same tolerant parser but is untested; **Windows-1252** (`CHARSET:1252`) transcoding
  (we currently treat bytes as UTF-8/ASCII); an optional **wrong-account guard** matching
  the file's `BANKACCTFROM` (sort code / account number) to the target account; and
  **multi-statement / multi-account** OFX files (v1 imports every `STMTTRN` into the
  account in the URL). _File: `internal/banking/statement_import.go`._
- **CSV import — format auto-detection + confirm-mapping LANDED; refinements remain.**
  The CSV path is no longer a fixed template: an upload is auto-detected (header synonyms →
  date/description/amount(s); signed vs money-in/out split; date-format sniff with a
  UK-first DD/MM-vs-MM/DD disambiguation) into a proposed `ColumnMapping`, surfaced via
  `POST …/import/preview` (writes nothing) and confirmed by the user in
  `BankStatementImportView` (per-field column dropdowns + date-format + a live preview)
  before commit; the optional running `Balance` column now fills `balance_minor`. _Files:
  `internal/banking/statement_detect.go`, `statement_preview.go`, `statement_import.go`,
  `handler.go`._ Deferred niceties:
  - **Multi-row preamble / footer stripping** — detection assumes the first non-blank row is
    the header (true for ~all UK exports); some banks prepend account-info lines or append a
    balance-summary footer. _File: `internal/banking/statement_detect.go`._
  - **Non-comma delimiters** (`;` / tab) — the parser is comma-only (stdlib `encoding/csv`
    default); sniff the delimiter from the header row. _File: `statement_import.go`._
  - **Parenthetical negatives** `(54.20)` in a signed amount column (some exports use them
    instead of a leading `-`). _File: `statement_import.go` (`parseSignedAmount`)._
  - **Remembered per-account mapping** — persist the confirmed mapping on the bank account
    so repeat uploads from the same bank can skip the confirm step. _File:
    `db/schema/banking_schema.sql`._
  - **Confident-detection one-shot** — when the detected mapping is unambiguous, offer to
    skip the confirm screen and import directly. _File: `BankStatementImportView.vue`._
- **Transaction reconciliation / "explain" — DATA LAYER + service + UI landed; refinements remain.**
  Built: the CoA (`categories`), the 18 `transaction_types`, the
  `transaction_type_categories` mapping + recompute trigger (data layer); the explain
  service (`internal/banking/explain.go` + `internal/categories` reference endpoints)
  with per-type validation, splitting + the over-explain guard, and VAT via
  `money.ComputeFixedVAT`; and the **inline expanding panel** on
  `BankAccountTransactionsView.vue` (Type → category/Transfer/User/Invoice pickers, VAT
  pre-fill from `default_vat`, add/edit/delete + split). v1 covers 13 of 18 types
  (`entity_link ∈ {NONE, BANK_ACCOUNT, USER, CAPITAL_ASSET, INVOICE}`). Refinements still to do:
  - **"Guess explanations" auto-engine.** Pre-suggest an explanation (the account's
    `guess_explanations` flag is stored but unused) — e.g. from the supplier→category
    dictionary or prior explanations; lands rows in `for_approval`.
  - **Inline category summary on collapsed explained rows** (today the status icon shows
    explained; you must expand to see the category) + **widen explain beyond owner/admin**
    (reading explanations is any-member-capable in the API but the panel is admin-gated).
  - **Double-entry ledger / posting engine.** "Which account per type-category pair"
    is currently reference metadata only — no journal lines are posted. A future ledger
    posts balanced debits/credits across nominals (incl. the per-entity sub-accounts
    750-x bank / 900-x user / 602-x asset that are represented by entity links today).
  - **Future-entity explanation links.** `INVOICE_RECEIPT` landed (2026-06-24) and `BILL_PAYMENT`
    landed (2026-06-25): `paid_invoice_id` / `paid_bill_id` on `bank_transaction_explanations`, wired to
    `invoices.paid_value_minor` / `bills.paid_value_minor`, `INVOICE` + `BILL` in `SupportedEntityLinks`.
    Remaining: `CREDIT_NOTE_REFUND` / `HP_PAYMENT` (+ refunds) are valid `type`s but still carry no
    dedicated link — add `paid_credit_note_id` / … to `bank_transaction_explanations` when those modules
    land. Likewise a capital-asset register (Purchase/Disposal) and `project_id` + rebilling.
  - **`second_sales_tax` / foreign-currency value** on explanations (FreeAgent has
    both; omitted from the v1 record).
  - **Seed review (Money IN/OUT tabs "as-is").** Confirm the codes seeded verbatim from
    the tabs where they differed from the detailed CoA sheets: user accounts 904 (sheet:
    Employer NI) / 905 (Employer Pension) / 908 (Expense Payment) / 907 (Drawings); 682
    vs 685 Other Debtors; 670 Share Premium; 604 disposal; 824 VAT OSS; 056; 797.
    _File: `db/seeds/transaction_type_categories.sql`, `db/seeds/categories.sql`._
- **Multi-currency balance display.** Accounts can be GBP / USD / EUR; the list's
  total balance vs the org `native_currency` needs conversion (depends on the
  `minor_unit`-aware money work in the Currencies backlog).
- **Closing-balance reconciliation check.** `bank_transactions.balance_minor`
  stores the bank-reported running balance; nothing yet asserts it matches our
  derived `opening + Σ(amount)`. A reconciliation report could flag drift.

## Projects (frontend / SPA)

- **Inline "add new contact" from the project form.** The New/Edit Project form's
  "Add a new contact" link currently navigates to `/contacts/new`, discarding any
  unsaved project input. Replace with an inline modal (or a draft-persistence /
  return-with-new-contact flow) so the half-filled project survives. _File:
  `web/src/views/ProjectEntryView.vue`._
- **"plus VAT" rendered as a checkbox.** The FreeAgent mockup shows "plus VAT" as
  static text beside the billing rate; we render it as a checkbox bound to
  `billing_rate_plus_vat` (so edit preserves the stored value). Revisit if a
  static "always plus VAT" presentation is preferred. _File:
  `web/src/views/ProjectEntryView.vue`._
- **Project delete from the UI.** `DELETE /api/v1/projects/:id` exists but the SPA
  exposes no delete affordance (list or form). Add when needed. _Files:
  `web/src/views/ProjectListView.vue`, `web/src/services/projects.service.ts`._

## Attachments & document storage

- **OCR retry / re-run + stale-PROCESSING recovery.** Smart Upload's OCR is
  fire-and-forget with no retry (`OcrService.Enqueue`): a crash mid-extraction
  leaves the row stuck in `PROCESSING`. Add (a) a startup sweep that resets stale
  `PROCESSING` rows via the `idx_expense_attachments_ocr` index, atomically
  claiming work with `UPDATE … WHERE ocr_status='PENDING'` so multiple Cloud Run
  instances don't double-process, and (b) a "re-run OCR" endpoint for FAILED
  captures. _Files: `ocr_service.go`, `db/queries/query.sql`._
- **Processor auto-detect / run-both.** Smart Upload currently asks the user to
  pick Receipt vs Invoice (`document_type`). Optionally auto-detect the document
  type, or run both processors and keep the higher-confidence result. _File:
  `ocr_documentai.go`._
- **UK VAT-number regex for the receipt path.** The Expense (receipt) parser has
  no structured supplier VAT field, so `SupplierVAT` is nil for receipts. Add a
  `GB` + 9/12-digit regex fallback over `ocr_raw_text` (+ checksum validation).
  _File: `ocr_documentai.go`._
- **Line-item storage.** `mapDocumentToResult` now reads `line_item/description`
  entities to assemble the expense description, but the line items themselves
  aren't persisted (only the derived description is). Capture them in a dedicated
  `expense_lines` table for a richer review screen / per-line VAT. _Files:
  `ocr_documentai.go`, `ocr_service.go`, schema (a new `expense_lines` table)._
- **Hybrid LLM fallback on low confidence.** When `ocr_confidence` is low, fall
  back to a multimodal LLM (Gemini on Vertex, in-region) for a second opinion.
  _File: `ocr_service.go`._
- **"Smart fill" onto an existing expense.** OCR only runs on Smart Upload
  captures. Optionally let a user re-run extraction over an attachment added to an
  *existing* expense to pre-fill its empty fields. _File: `attachment_service.go`._
- **Richer capture state / DB-enforced completeness.** `needs_review` is a single
  boolean. If a fuller capture lifecycle is wanted, upgrade to a `capture_status`
  enum (AWAITING_OCR→AWAITING_REVIEW→CONFIRMED). Separately, skeleton drafts use
  placeholder values for the NOT NULL money/date/category columns; the stricter
  alternative is making them nullable + a CHECK requiring them once
  `needs_review=false`, at the cost of a wider nullable-type change. _Files:
  `db/schema/schema.sql`, `expense_service.go`._
- **Thumbnails / previews.** Generate a small downscaled image per attachment so
  the SPA's list view doesn't fetch full-size files. This is image *resizing* for
  UX, separate from storage. _File: `attachment_service.go`._
- **CMEK (customer-managed encryption keys).** Attachments rely on GCS default
  at-rest encryption. For a stronger key-custody compliance story, point the
  bucket at a Cloud KMS key — a bucket-level setting, no code change.
- **DB-level single-primary guarantee.** "Exactly one primary per expense" is
  enforced in the service inside a transaction. A partial unique index
  (`... ON expense_attachments (expense_id) WHERE is_primary`) would enforce it in
  the database too, but needs handling for the (low-risk) concurrent-first-upload
  race. _File: `db/schema/schema.sql`._
- **Orphaned-object reconciliation.** Upload cleans up its own object if the
  metadata write fails, and delete is best-effort, but a crash between steps can
  still strand a GCS object (or leave a deleted row's file behind). Add a periodic
  sweep reconciling `expense_attachments` against the bucket. _File:
  `attachment_service.go`._
- **Virus/malware scanning.** Receipts are user-uploaded files; consider scanning
  on upload (ClamAV step, or a GCS-triggered scan) before they're downloadable.
- **HEIC support.** iOS photos are often HEIC, which `http.DetectContentType`
  reports as `application/octet-stream`, so they're currently rejected. Add HEIC to
  the allowlist (and likely transcode to JPEG). _File: `attachment_service.go`._
- **Direct-to-GCS uploads.** Uploads are server-proxied today (simple, full
  server-side validation). If server bandwidth becomes a constraint, add a
  signed-upload-URL flow so browsers upload straight to GCS. _Files:
  `attachment_service.go`, `attachment_handler.go`._
- **Audit log on attachment create/delete.** `UploadAttachment` / `DeleteAttachment`
  leave the same `CreateAuditEntry` TODO as `CreateExpense`. _File:
  `attachment_service.go`._

## Email-to-expense (Mailgun inbound)

- **Record the real submitter as `created_by`.** v1 sets both the claimant and
  `created_by_user_id` to the inbox owner; the actual sender survives only in
  `inbound_email_events.sender`. Extend `CaptureFromReceipt` with a
  claimant/creator split (matching the on-behalf model) and pass the resolved
  sender. _Files: `attachment_service.go`, `email_inbox_service.go`._
- **Durable retry for background processing.** The webhook now ACKS Mailgun after a
  fast synchronous claim and does the capture/render work in a background goroutine
  (`EmailInboxService.Accept` → `runInBackground`), so the POST no longer times out.
  The tradeoff: a transient failure in the background is logged and marks the
  `inbound_email_events` row `error`, but Mailgun has already been 200'd and will NOT
  re-deliver — so there is no automatic retry anymore. Replace the fire-and-forget
  goroutine with a durable queue (Pub/Sub, as the FreeAgent push already uses) and/or
  a sweep that reprocesses stuck `received`/`error` rows (the content-hash dedupe
  keeps a re-run from duplicating drafts; like the OCR stale-`PROCESSING` item).
  _Files: `email_inbox_service.go`, `db/queries/email_inbox.sql`._
- **Cloud Run CPU for post-ack work.** The background goroutine (above) — and the
  existing OCR `Enqueue` goroutine — need CPU after the response returns. Run the
  service with "CPU always allocated" (or min-instances ≥ 1) so post-ack processing
  isn't throttled/paused. _Ops (Cloud Run config)._
- **Concurrent-duplicate race (TOCTOU).** The content dedupe is read-then-check,
  so two *identical* emails arriving truly concurrently could both pass the check
  before either's draft commits → a duplicate. Tighten with an advisory lock keyed
  on `(org, claimant, content_hash)` or a careful unique constraint if it bites.
  _File: `email_inbox_service.go`._
- **Fuzzy / amount-based dedupe.** The content hash only catches the *identical*
  file; a re-scanned or re-photographed receipt (different bytes, same purchase)
  slips through. After OCR fills supplier+amount+date, optionally flag likely
  duplicates for review. _Files: `ocr_service.go`, `expense_service.go`._
- **Backfill `content_hash`.** Attachment rows created before this column are NULL,
  so cross-history dedupe misses them. A one-off job could re-read each GCS object
  and populate the hash. _File: `attachment_service.go`._
- **Oversized emails.** The webhook parses inline attachments and caps the body at
  `maxInboundEmailBytes` (35 MiB); a larger email fails to parse. Add a
  store-and-fetch fallback (Mailgun `store()` + retrieve via the API) for emails
  over the forward limit. _File: `email_inbox_handler.go`._
- **Keep the original message.** Persist the raw HTML/.eml alongside the rendered
  `email-body.pdf` for audit and re-extraction. _Files: `email_inbox_service.go`, schema._
- **Better HTML extraction.** For HTML-body receipts a text/LLM extractor may beat
  OCR-on-rendered-PDF; also add an "is this actually a receipt?" heuristic so
  non-receipt body emails don't create junk drafts. _Files: `email_inbox_service.go`, `ocr_service.go`._
- **Anti-spoofing.** The sender gate trusts the (spoofable) `From` header. Enforce
  Mailgun's DKIM/SPF/DMARC verdicts before accepting. _File: `email_inbox_handler.go`._
- **SSRF hardening for rendering.** Gotenberg renders arbitrary email HTML, which
  can fetch referenced URLs. Run it network-isolated and disable/limit
  remote-resource fetching. _Ops + `html_renderer.go`._
- **Multi-recipient fan-out.** An email addressed to several of our inbox addresses
  currently captures only the first match; optionally create a draft for each
  addressed member (needs draft-level dedupe per recipient). _File: `email_inbox_service.go`._
- **Address rotation.** The inbox address is read-only in v1; add a
  regenerate/revoke endpoint for a leaked address. _Files: `email_inbox_service.go`, `db/queries/auth.sql`._
- **Abuse rate-limiting.** The public webhook is unthrottled; add per-sender /
  per-address limits. _File: `email_inbox_handler.go`._
- **`inbound_email_events` retention.** The event log holds PII (sender/recipient/
  subject); add a retention/purge policy. _Files: schema + a periodic sweep._

## Integrations (FreeAgent push)

- **Encrypt integration secrets at rest.** The GLOBAL app `client_secret`
  (`provider_credentials`) and each org's `access_token` / `refresh_token`
  (`organisation_integrations`) are stored in plaintext — including the HMRC MTD
  tokens, which live here (not on `organisations`). Encrypt before production —
  pgcrypto `pgp_sym_encrypt()` or a KMS-backed approach. _Files:
  `db/schema/integrations_schema.sql`, `integration_service.go`._
- **Transactional outbox for `expense.approved`.** The planned publish (B1) is
  best-effort in the request path; a publish failure loses the event (recoverable
  only via the manual re-push). Add an outbox table written in the approve
  transaction + a sweeper that publishes, for guaranteed delivery. _Files:
  `expense_status.go`, schema._
- **Provision the shared CoA per org.** Only the dev org (`00000000-…-0001`) is
  seeded with the FreeAgent-coded chart (`db/seeds/categories.sql`); new orgs get no
  `categories` automatically, so their expense/bill pickers are empty and their
  pushes have nothing valid to map. Seed the CoA on org creation. _Files:
  org-provisioning path, `db/seeds/categories.sql`._
- **Per-provider category mapping (don't equate `nominal_code` with the provider's code).**
  The push maps the shared CoA's `categories.nominal_code` straight to a FreeAgent
  category URL — and since the CoA IS seeded from FreeAgent's chart (e.g. Travel =
  `254`), that maps cleanly today (the 2026-06-30 expenses→CoA unification made this
  more correct, dropping the old divergent `expense_categories` codes). A second
  provider (Xero/QuickBooks) uses a different chart, so add a per-(provider, category)
  mapping (column or table) and have the internal expense-for-push endpoint emit the
  provider's code rather than our raw `nominal_code`. _Files: `internal/integrations/workflow.go`,
  `db/queries/integrations.sql`, schema._
- **Remaining FreeAgent push work** (Pub/Sub publish, internal OIDC endpoints, the
  Cloud Workflow + Eventarc, manual re-push, user-ref cache, manual user-mapping
  UI, push-status read, reverse sync) — see the approved plan at
  `~/.claude/plans/i-d-like-to-push-peppy-papert.md`. Add/remove as each lands.

## Overview dashboard (cards)

The "Overview" page (`/overview`) is a tabbed container — a new financial Overview
dashboard + the existing VAT dashboard as a 2nd tab. The **tabbed shell** shipped
(`web/src/views/OverviewDashboardView.vue` + `OverviewPanel.vue` placeholder +
`VatDashboardPanel.vue`). The **read-only `internal/overview` domain** (mirrors the
cross-domain read pattern of `internal/vat`) + the **Cashflow**, **Invoice
Timeline** and **Banking** cards have SHIPPED: `db/queries/overview.sql` + sqlc
block → `db/overview`, `internal/overview` (dto/service/handler) wired in `main.go`,
`chart.js` via PrimeVue's `Chart` on the frontend, with PER-CARD endpoints
(`GET /api/v1/overview/{cashflow,invoice-timeline,banking}`). Remaining card(s) each
add a sibling query + GET route + an `OverviewPanel.vue` card:

- **Cashflow** — ✅ SHIPPED (`GET /api/v1/overview/cashflow`, the `CashflowByMonth`
  `generate_series` query, the OverviewPanel bar chart + Incoming/Outgoing/Balance
  totals; tested in `overview_service_test.go`). Deferred refinements: a period
  selector (the "Last 12 months" label is static); netting out inter-account
  `TRANSFER` explanations + excluding `is_personal` accounts (v1 counts raw amounts
  across all accounts, so a big internal transfer can spike a month); and folding
  the per-card endpoints into one composite `GET /api/v1/overview` if round-trips
  matter.
- **Banking** — ✅ SHIPPED (`GET /api/v1/overview/banking`, the `BankBalanceByMonth`
  cumulative query + `BankBalanceSummary`, the OverviewPanel line/area chart + the
  "All accounts" balance + Add account / View all bank accounts footer; tested in
  `overview_service_test.go`). Balance is derived (Σ `opening_balance_minor` + Σ
  `amount_minor`), matching the Bank Accounts page. Deferred refinements: the mock's
  per-account "All accounts" selector (v1 aggregates all live accounts); an account-
  scoped "Upload statement" action (replaced here with Add account); and a period
  selector / daily granularity (v1 = month-end points, last 12 months).
- **Invoice Timeline** — ✅ SHIPPED (`GET /api/v1/overview/invoice-timeline`, the
  `InvoiceTimelineByMonth` + `OutstandingInvoiceTotal` queries, the OverviewPanel
  stacked bar chart + Outstanding total + New invoice / View all footer; tested in
  `overview_service_test.go`). SENT invoices' whole total is bucketed by month into
  Overdue / Due / Paid matching `deriveDisplayStatus` (so Overdue needs a set,
  past `due_on`; a no-due-date invoice is "Due"). Deferred refinements: month
  paging (`‹ ›`); SCHEDULED invoices + the mock's Estimates/Timeslips sub-tabs;
  and an amount-split view (a part-paid overdue invoice currently books its FULL
  total to Overdue).
- **Expenses & Bills** — recent expenses list (`ListRecentExpenses`,
  `db/queries/query.sql`, joined to category name) + "Balance Owed"; Bills
  sub-tab via `bills.due_value_minor`.
- **Profit & Loss** — DEFERRED beyond the others: needs a Dividends data source
  and an org-level Corp Tax rate (neither exists yet). Income (SENT invoices) +
  Expenses (approved expenses + bills) + Operating profit are computable now.
- Card-level sub-tabs shown in the FreeAgent mock that have no feature behind
  them yet (Estimates, Timeslips) are out of scope.

Optional polish once cards exist: deep-link the active tab via a `?tab=` query
param (the shell currently uses a local `ref`), and a top-right "Add new"
quick-action menu.

## Reports (Trial Balance + future)

The reports surface (`internal/reports`, the SPA's `Reports` nav group) has shipped
two reports: **Trial Balance** (`GET /reports/trial-balance` → `TrialBalanceView.vue`,
a cumulative-from-inception today snapshot) and **Account Transactions**
(`GET /reports/account-transactions` + `/reports/accounts` → `AccountTransactionsView.vue`,
the per-account drill-down; the Trial Balance codes link into it). Deferred:

- **Accounting-year boundary / true FreeAgent TB semantics.** Iteration 1 sums all
  journal lines on/before the date (a valid, balancing cumulative TB). FreeAgent's
  Trial Balance shows current-year P&L *movement* with prior-year P&L rolled into a
  retained-earnings/opening-balance figure, while balance-sheet accounts stay
  cumulative. Needs the org's accounting-year start + a retained-earnings posting.
- **Real date pickers / accounting-year presets.** The TB `?date=` param and the
  Account Transactions `?from=/?to=` params are plumbed end-to-end; the TB Date
  control is a single "Today" option, and Account Transactions' "Year to date"
  preset uses the **calendar** year (no stored accounting-year start). Add a custom
  date picker + true accounting-year presets once the org carries a year-start.
- **Account Transactions: more source links + filters.** Description links cover
  INVOICE / INVOICE_RECEIPT / EXPENSE / BILL / BILL_PAYMENT; BANK_EXPLANATION /
  BANK_TRANSFER (need a bank-account id, not just `source_id`), PAYROLL, MONEY_USER,
  BANK_OPENING, MANUAL render as plain text. The mockup's **"Has attachments"**
  filter and **Export Report** button + layout-toggle icons were dropped for v1.
  An **all-accounts (multi-section)** view is also deferred (one account at a time now).
- **Natural-numeric code ordering.** `ORDER BY nominal_code` is text order (correct
  for today's zero-padded `001`…`908-1`); revisit if codes outgrow that.
- **Multi-base-currency orgs.** The reports sum `base_amount_minor` and label the
  result with the org's current `native_currency`; they assume a single base
  currency across all entries (fine today — entries snapshot the base at post time).
- **More reports.** P&L and Balance Sheet are the natural next siblings in
  `internal/reports` + the Reports nav group.

## Cleanups (also flagged as background tasks)

- **Extract `AttachmentService` out of `package main` → `internal/attachments`.**
  Deferred deliberately (2026-06-21) after extracting `internal/storage` +
  `internal/ocr`. The blocker: `AttachmentService.CaptureFromReceipt` (Smart Upload)
  returns `*ExpenseDetailResponse` and calls `buildExpenseDetail` — both root
  **expense-domain** code (`server.go`, `expense_service.go`) that `internal/`
  can't import. Clean extraction needs either (a) decoupling capture so it returns
  just the new expense+attachment IDs (the Smart Upload handler + `/expenses/capture`
  route stay in root and build the detail), or (b) extracting the expense domain
  first (`ExpenseDetailResponse` + `buildExpenseDetail` → a shared/expenses package).
  Until then `attachment_service.go` + `attachment_handler.go` stay in root (and on
  the `arch_test.go` allowlist). Note: attachments already imports `internal/storage`
  + `internal/ocr` (for `ocr.ValidDocumentType`), so only the expense-DTO coupling
  remains. Revisit when the expense domain is extracted.

- **Strip `[DEBUG]` token logging.** `token/paseto_maker.go` `VerifyToken` logs
  the full bearer token (plus payload) on every authenticated request — a replay
  risk — and `token/payload.go` `Valid()` prints debug lines. Remove them.
  _Flagged as a background-task chip on 2026-06-11._

## Pre-existing TODOs noted in code (not introduced by recent work)

- **Wire up the expense audit log (all mutations).** The `expense_audit_log`
  table (`db/schema/schema.sql`) is entirely unwired — nothing fills it: no DB
  trigger, and no Go writes it across *any* expense mutation. `CreateExpense`
  has a placeholder for a `CreateAuditEntry` INSERT, and the approval-workflow
  transitions (`ChangeExpenseStatus`: submit/approve/reject/reopen) likewise
  record no history beyond the columns on the row itself (only
  `approved_by_user_id` captures an actor; there is no submitted_by/rejected_by).
  Wire it once for all mutations — either a single DB trigger on `expenses`, or
  audit inserts inside each service transaction (create/update/delete/status).
  _Files: `expense_service.go`, `expense_status.go`, `db/queries/query.sql`,
  `db/schema/schema.sql`._
- **Structured logging.** Handlers carry `_ = appErr.Error()` placeholders for a
  real logger (slog/zap) instead of `log`/`fmt`. _Files: `server.go`,
  `auth_handler.go`._

## HMRC Making Tax Digital

- **HMRC fraud-prevention headers — DONE for the data APIs; Phase-2 items remain.**
  The full `WEB_APP_VIA_SERVER` header set is now assembled per request (`internal/vat/fraud.go`,
  browser signals via `web/src/lib/fraudSignals.ts`) and applied to every HMRC data call
  (submit + dashboard + reconcile), validated against HMRC's Test Fraud Prevention Headers
  API (no errors). Still to do before/at production:
  - **Provision Cloud Run static egress** + set `HMRC_VENDOR_PUBLIC_IP` (blank today, so
    `Gov-Vendor-Public-IP`/`Gov-Vendor-Forwarded` are omitted).
  - **`Gov-Client-Public-Port` behind the proxy.** We derive it from `RemoteAddr` — correct
    for a direct connection, but behind Cloud Run's proxy that's the proxy's port, not the
    user's. HMRC documents this private-network case as "contact us to explain".
  - **Inform HMRC of the two legitimate omissions:** `Gov-Client-Multi-Factor` (we're
    single-factor email+password) and `Gov-Vendor-License-IDs` (no vendor licences). Both
    are HMRC warnings that need a written explanation at onboarding, not code.
  - **Fraud headers on the OAuth token calls** (`internal/integrations/hmrc/client.go`
    `postToken`): the connect/callback are browser redirects (no `X-Client-Fraud-Signals`)
    and refresh runs server-side; best-effort headers there are deferred.
- **Register the redirect URI in HMRC Developer Hub.** The sandbox app must have
  `{API_PUBLIC_URL}/api/v1/hmrc/callback` (and `http://localhost:8080/api/v1/hmrc/callback`
  for local dev) registered under Applications → Redirect URIs.
- **Fetch obligations from HMRC in `ListPeriods()`.** Periods are still generated
  locally from VAT settings. The connect-time **period reconciliation**
  (`internal/vat/reconcile.go`: `CheckHMRCPeriods` / `SyncHMRCPeriods`, surfaced as
  the Accept/Reject modal on the Integrations page) now rewrites the settings to
  match HMRC's obligations, so the generated schedule lines up with HMRC going
  forward — but it's a point-in-time sync, anchored to HMRC's earliest **visible**
  obligation (≤360-day window). A fuller fix would have `ListPeriods` read HMRC's
  obligations live (cached) when connected, with `resolvePeriod` reading the same,
  falling back to local generation when offline — so the list never drifts and
  isn't truncated to the obligations window. _Files: `internal/vat/service.go`
  (`ListPeriods`, `resolvePeriod`), `internal/vat/reconcile.go`._
- **Period reconciliation history-truncation.** `SyncHMRCPeriods` anchors
  `effective_date` to HMRC's earliest visible obligation, so for an established org
  the regenerated period list starts from that window (older periods drop off). The
  modal warns when already-filed `vat_returns` would be affected but does not block.
  Revisit if/when historical period reporting matters. _File: `internal/vat/reconcile.go`._
