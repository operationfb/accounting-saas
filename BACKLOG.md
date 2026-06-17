# Backlog / Deferred Items

A running list of work that was intentionally deferred, plus notable TODOs found
in the code. Add to this file whenever you defer something so it isn't lost in a
commit message or chat. Remove items as they're completed.

_Last updated: 2026-06-17_

## Auth & authorization

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

- **List filtering & pagination for `GET /api/v1/expenses`.** Today the endpoint returns the full set the caller may see (owner/admin: whole org; others: own) with no filters or paging. Add query-param filtering (date range, status, project) and pagination (limit/offset or cursor), preserving the existing owner/admin-vs-own scoping in `ListExpenses`. Reuse the existing org-scoped sqlc queries `ListExpensesByDateRange` / `ListExpensesByStatus` / `ListExpensesByProject` (`db/queries/query.sql`); user-scoped + filtered variants would need new queries. _Files: `expense_service.go`, `server.go`, `db/queries/query.sql`._


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
- **Store category VAT default.** The category screenshots distinguished
  "normally VATable" vs "normally Zero-VAT"; we stored only `category_group`,
  not the VAT default. Consider adding a `normally_vatable` / default-VAT hint to
  `expense_categories` to pre-fill an expense's VAT status from its category.
  _Files: `db/schema/schema.sql`, `expense_service.go` (`CreateExpense`)._
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
- **Link expenses/invoices to a contact.** Contacts exist but nothing references
  them yet. When building invoices (or attributing an expense to a supplier), add
  a nullable `contact_id` FK + the lookup. _Files: schema, `expense_service.go` /
  a future invoice service._
- **List filtering, search & pagination for `GET /api/v1/contacts`.** Returns the
  whole org's contacts, ordered by name only, unpaged. Add name/email search, an
  active-only filter, and pagination. _Files: `contact_service.go`, `server.go`,
  `db/queries/contacts.sql`._

## Organisation / Company details

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
- **Recover/refine stalled ingests.** A webhook that 500s after claiming the
  Message-Id but before finishing leaves a non-terminal `inbound_email_events`
  row; Mailgun's retry reprocesses it, which can duplicate any drafts already
  created on the first attempt. Add per-attachment idempotency (key drafts by
  Message-Id + index) and a sweep for stuck `received`/`error` rows (like the OCR
  stale-`PROCESSING` item). _Files: `email_inbox_service.go`, `db/queries/email_inbox.sql`._
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

## Cleanups (also flagged as background tasks)

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
- **Encrypt MTD OAuth tokens at rest.** `organisations.mtd_access_token` /
  `mtd_refresh_token` are stored in plaintext; the schema flags encrypting them
  before production. _File: `db/schema/auth_schema.sql`._
