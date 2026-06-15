# Backlog / Deferred Items

A running list of work that was intentionally deferred, plus notable TODOs found
in the code. Add to this file whenever you defer something so it isn't lost in a
commit message or chat. Remove items as they're completed.

_Last updated: 2026-06-14_

## Auth & authorization

- **On-behalf expense creation.** Today a user can only create expenses for
  themselves — `CreateExpense` forces `user_id` = `created_by_user_id` = the
  authenticated caller. The schema deliberately separates `created_by_user_id`
  from the claimant `user_id` to allow an admin to enter an expense on behalf of
  another user. Wire this up (e.g. an optional `on_behalf_of_user_id` in the
  request, permitted only for owner/admin). _File: `expense_service.go`
  (`CreateExpense`)._

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

## Attachments & document storage

- **OCR / data capture for receipts.** The `expense_attachments` table already
  carries the phase-2 OCR columns (`ocr_status`, `ocr_raw_text`,
  `ocr_extracted_data`, `ocr_processed_at`) and the `UpdateAttachmentOCRStatus`
  query, but nothing populates them. Wire up a background pipeline that OCRs
  uploaded receipts and fills these in (Datamolino-style auto-fill). _Files:
  `attachment_service.go`, `db/queries/query.sql`._
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

## Cleanups (also flagged as background tasks)

- **Strip `[DEBUG]` token logging.** `token/paseto_maker.go` `VerifyToken` logs
  the full bearer token (plus payload) on every authenticated request — a replay
  risk — and `token/payload.go` `Valid()` prints debug lines. Remove them.
  _Flagged as a background-task chip on 2026-06-11._

- **Fix the dev seed password hash.** The `dev@example.com` seed row in
  `db/schema/auth_schema.sql` ships a placeholder `password_hash` that does NOT
  match the documented `devpassword123`. Replace it with a correct bcrypt
  (cost 12) hash so freshly-seeded databases can log in. (The shared dev DB row
  was already corrected by the login test; this is for fresh seeds.) _Flagged as
  a background-task chip on 2026-06-11._

## Pre-existing TODOs noted in code (not introduced by recent work)

- **Expense audit log on create.** The create transaction has a placeholder for
  an audit-log INSERT (`CreateAuditEntry` query + call) that isn't implemented.
  _File: `expense_service.go` (`withTransaction`)._
- **Structured logging.** Handlers carry `_ = appErr.Error()` placeholders for a
  real logger (slog/zap) instead of `log`/`fmt`. _Files: `server.go`,
  `auth_handler.go`._
- **Encrypt MTD OAuth tokens at rest.** `organisations.mtd_access_token` /
  `mtd_refresh_token` are stored in plaintext; the schema flags encrypting them
  before production. _File: `db/schema/auth_schema.sql`._
