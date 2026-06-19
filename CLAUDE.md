# CLAUDE.md

This file gives Claude Code the context it needs to work effectively in this repository. It is the source of truth for how this project is structured, how to build features, and how to write/run code in it.

## Project Overview

This is the backend for a UK-focused accounting SaaS platform (think FreeAgent / Datamolino for SMEs). It is in **early active development**, starting with the **expenses module**.

The developer (Aydin) is a **novice programmer who reads and wants to understand every piece of code**. This is a learning-first project as much as a product-building project.

### Implications for how Claude Code should behave

- **Explain as you go.** Prefer well-commented code with short rationale comments (`// why`, not just `// what`) over terse "clever" code.
- **Incremental over clever.** Favor simple, readable solutions that can be improved later over premature abstraction or "enterprise" patterns the project doesn't need yet.
- **Architecture before code.** For anything non-trivial (new module, schema change, new external integration), propose the design/approach first and get confirmation before writing a lot of code.
- **One feature/module at a time.** Keep changes scoped. Don't refactor unrelated code while implementing a feature unless asked.
- **Surface tradeoffs.** When there are multiple reasonable ways to do something, briefly explain the options and why you picked one.

## Tech Stack

### Backend
- **Language:** Go
- **Web framework:** Gin
- **Database:** PostgreSQL
- **DB access:** sqlc (generates type-safe Go from SQL) + pgx/v5 driver
- **Money handling:** shopspring/decimal — all monetary values are stored as **integers in minor units (pence)** in the database and converted to/from decimal strings at API boundaries. **Never use float for money.**

### Frontend (separate concern, referenced for context)
- Vue.js (Vite SPA), TypeScript
- Dinero.js for money arithmetic
- TanStack Query, Vee-Validate + Zod, openapi-typescript

### Infrastructure
- GCP Cloud Run, `europe-west2` (London) — UK data residency requirement
- TrueLayer for Open Banking / bank feed ingestion (planned)

### Compliance context (keep in mind when designing schemas/APIs)
- GDPR
- HMRC Making Tax Digital (MTD)
- FCA Open Banking regulations

## Project Structure

Single Go module (`github.com/operationfb/accounting-saas`) — a monolith organized by domain. The HTTP/application layer is `package main` at the repo root; database access is split into per-domain sqlc-generated packages under `db/`.

```
.
├── main.go              # Entry point: load .env/config, open pgx pool, build deps, start server
├── server.go            # Gin engine + Server struct + route registration (incl. the public Mailgun webhook + /inbox-address) + expense & contact handlers
├── auth_handler.go      # AuthHandler: POST /api/v1/auth/login → PASETO token + sanitised user
├── expense_service.go   # Expense business logic (validation, money conversion, DB orchestration)
├── expense_status.go    # Approval-workflow state machine: status constants, the transition table, and ChangeExpenseStatus (submit/approve/reject/reopen)
├── contact_service.go   # Contact business logic + request/response DTOs (CRUD, validation, creator/admin auth) for the contacts module
├── organisation_service.go  # OrganisationService: read/update the org's own "Company Details" (GET/PUT /api/v1/organisation; member read, owner/admin edit)
├── user_service.go      # UserService: read/update the caller's own "My Details" (GET/PUT /api/v1/profile; first/last name — always self-scoped from the token, no role check)
├── attachment_service.go    # Receipt-attachment logic: authorise, validate, store bytes in GCS, metadata in DB, primary-file rule; plus CaptureFromReceipt ("Smart Upload")
├── attachment_handler.go    # HTTP handlers for attachment endpoints (multipart upload, list, download URL, set-primary, delete) + Smart Upload capture
├── ocr_service.go       # OcrService: background receipt/invoice extraction — drives the attachment ocr_status machine, fills the expense (DocumentExtractor interface + ExtractionResult)
├── ocr_documentai.go    # documentAIExtractor: Google Document AI implementation of DocumentExtractor (Invoice + Expense parsers, EU regional endpoint, MoneyValue→pence)
├── storage.go           # Storage interface (Upload / Download / SignedDownloadURL / Delete / Bucket) — abstraction over the object store
├── storage_gcs.go       # gcsStorage: the Google Cloud Storage implementation of Storage
├── inbound_email.go     # Email-to-expense: provider-neutral InboundEmail/InboundAttachment types + Mailgun HMAC signature check
├── html_renderer.go     # HTMLRenderer interface + gotenbergRenderer (HTML email body → PDF, for receipts that arrive with no attachment)
├── email_inbox_service.go   # EmailInboxService: Ingest (dedupe → route → sender-check → capture → HTML fallback) + GetOrCreateInboxAddress
├── email_inbox_handler.go   # HTTP handlers: POST /webhooks/mailgun/inbound (public, HMAC) + GET /inbox-address (authed)
├── errors.go            # AppError type + ErrorCode constants (incl. bad_request) + handler→HTTP mapping
├── handler_helpers.go   # Handler-layer helpers: respondError (the single JSON error-writer; logs 500s via slog), bindJSON (bind + standard 400 envelope), logInternalError
├── authz.go             # authorizeMember: the shared active-membership check + role lookup that every service's authorize() delegates to
├── server_test.go       # Integration tests (real Postgres) for the HTTP handlers
├── attachment_service_test.go   # AttachmentService tests (real Postgres + real GCS dev bucket)
├── ocr_service_test.go  # OCR/Smart Upload tests (real Postgres + GCS; Document AI faked) + money-conversion unit test
├── supplier_category_test.go  # supplier→category dictionary: learn-trigger tests (DB) + auto-categorise tests (Postgres + GCS)
├── contact_service_test.go  # Contacts CRUD tests (real Postgres): happy path, defaults, 0-vs-NULL terms, validation, auth, multi-tenant isolation
├── organisation_service_test.go  # Company Details tests (real Postgres): update round-trip, member read, owner/admin-only edit, validation, field preservation, isolation
├── user_service_test.go  # My Details tests (real Postgres): self-scoped GET, update round-trip + persist, phone/avatar preservation, 400/422 name validation, 401 unauth
├── expense_status_test.go  # Status state-machine tests (real Postgres): each transition + its column effects, 409 illegal moves, authz (admin-only vs claimant), 400/422 validation, 404 isolation
├── email_inbox_test.go  # Email-to-expense tests (real Postgres + GCS; Mailgun & Gotenberg faked): routing, sender/cross-tenant auth, capture, HTML body, dedupe, signature, address generation
├── sqlc.yaml            # sqlc config — one generation block PER domain (expenses, auth, contacts, projects, email_inbox, integrations)
│
├── db/
│   ├── schema/          # Source-of-truth DDL (full CREATE TABLE files, not migrations)
│   │   ├── schema.sql        # expenses module, supplier_category_map dictionary, set_updated_at() + learn_supplier_category() triggers
│   │   ├── auth_schema.sql   # auth module: users, organisations (incl. Company Details fields), organisation_memberships
│   │   ├── contacts_schema.sql  # contacts module: customers/suppliers (invoicing details, charge_vat, bank)
│   │   └── email_inbox_schema.sql  # email-to-expense: inbound_email_events (dedupe + audit). The inbox ADDRESS columns live on organisation_memberships in auth_schema.sql
│   ├── queries/         # Annotated SQL = sqlc input (one file per domain)
│   │   ├── query.sql         # expenses queries
│   │   ├── auth.sql          # auth queries (CreateUser, GetUserByEmail, memberships, ...)
│   │   ├── contacts.sql      # contacts queries (Create / Get / List / Update / SoftDelete)
│   │   └── email_inbox.sql   # email-to-expense queries (Claim / Finish / GetByMessageID for the inbound-email event log)
│   ├── seeds/           # Reproducible seed data (e.g. expense_categories.sql)
│   ├── expenses/        # GENERATED (package expenses) — never hand-edit
│   ├── auth/            # GENERATED (package auth) — never hand-edit
│   ├── contacts/        # GENERATED (package contacts) — never hand-edit
│   └── email_inbox/     # GENERATED (package email_inbox) — never hand-edit
│
├── token/              # PASETO authentication tokens
│   ├── maker.go              # Maker interface (CreateToken / VerifyToken)
│   ├── paseto_maker.go       # PasetoMaker implementation (PASETO v2 local)
│   └── payload.go            # Token Payload (UserID, OrganisationID, expiry)
│
├── money/
│   └── money.go         # Shared money kernel (pure, no DB): pence↔pounds, PoundsToMinor (half-up), BpsToPercent, ComputeFixedVAT (HMRC VAT fraction), ClampToInt32 — reused by expenses, projects & the future invoices module
│
└── util/
    └── random.go        # Random test-data helpers for the integration tests
```

Config/tooling at the root: `go.mod` / `go.sum` (deps), `.env` (local config — `DATABASE_URL`, `PASETO_SYMMETRIC_KEY`, `PORT`, `GCS_BUCKET` for receipt-attachment storage, `DOCAI_*` for Document AI OCR, `INBOX_DOMAIN` + `MAILGUN_INBOUND_SIGNING_KEY` for the email-to-expense channel, and `GOTENBERG_URL` for HTML-body rendering), `.gitignore`, and this `CLAUDE.md`.

### Money (shared conversion kernel)

All conversions between the integer **minor units (pence)** the DB stores and the **decimal pound strings** the API exposes live in one place — the `money` package (`money/money.go`), reused by expenses, projects and the upcoming invoices module. It depends only on `shopspring/decimal` (no `pgtype`, no proto), so it stays a clean **pure** kernel with fast unit tests (`money/money_test.go` — the first pure unit tests in the repo).

- **Functions:** `MinorToPounds(int64)→"42.50"`, `PoundsToMinor("42.50")→4250`, `BpsToPercent`, `ComputeFixedVAT` (the HMRC VAT-fraction extraction from a VAT-inclusive total), `ClampToInt32`.
- **int64-based.** Pence fits int32 for a single expense (≈£21.4m ceiling) but invoice/billing totals can exceed it; int32-column callers cast on the way out (optionally guarded by `ClampToInt32`).
- **Rounding rule (a decision worth knowing):** `PoundsToMinor` rounds **half-up and accepts any precision** (`"42.999" → 4300`). It is the one canonical pounds→pence conversion and replaced three divergent inline copies (two truncated, one rounded), so the rule is now decided exactly once.
- **Deliberately left out:** DB-type glue (`pgNullText`, `pgNumericFromDecimal`, …) and the proto-unpacking `moneyToMinor` (Google `MoneyValue`, in `ocr_documentai.go`) stay with their callers — the kernel takes no DB or proto dependency.

### Attachment storage (receipts)

Expense attachments (PDF/image receipts) follow the standard split: the **file bytes live in Google Cloud Storage**, the **file metadata lives in Postgres** (`expense_attachments`). The service (`attachment_service.go`) depends on the `Storage` interface (`storage.go`); the only implementation is GCS (`storage_gcs.go`), reached via Application Default Credentials. The DB row stores the GCS object **key** in `storage_path` (never a signed URL — those are short-lived and generated on demand). GCS and Postgres are not one transaction, so an upload writes to GCS first and, if the metadata write fails, best-effort deletes the object to avoid orphans. The **first file uploaded to an expense is the primary** one; deleting the primary promotes the oldest remaining file.

### OCR / "Smart Upload" (Document AI)

There are two upload paths. **"Add file"** (`POST /expenses/:id/attachments`) attaches a receipt to an *existing* expense and runs **no** OCR. **"Smart Upload"** (`POST /expenses/capture`) is receipt-first: it creates a **skeleton draft** (`needs_review=TRUE`, placeholder Sundries category, `gross=0`), attaches the file, and kicks off **background OCR** (`OcrService.Enqueue` → a goroutine). The user picks Receipt or Invoice (`document_type`), which routes to the matching Document AI processor (Expense vs Invoice parser); residency is enforced by the **`eu` regional endpoint**. OCR drives the attachment's `ocr_status` (PENDING→PROCESSING→COMPLETE/FAILED/SKIPPED) and **COALESCE-fills only empty expense fields** — it never overwrites user-entered data and never clears `needs_review` (a human confirms by saving, which clears it via the normal update). `needs_review` is a **third axis**, orthogonal to the approval `status` and the attachment `ocr_status`. The Document AI call sits behind the `DocumentExtractor` interface (`ocr_documentai.go`), so tests fake it (like the only-mock-external-services rule) while still using real Postgres + GCS; money is converted `MoneyValue`→pence with `shopspring/decimal` (HALF_UP). OCR is optional: with `DOCAI_*` unset, Smart Upload still creates drafts but they stay PENDING. An **opt-in** integration test (`TestDocumentAILive`, gated on `DOCAI_LIVE_TEST=1` so routine runs aren't billed) exercises the *real* API against both processors: `DOCAI_LIVE_TEST=1 go test -run TestDocumentAILive -v`.

### Supplier → category dictionary (auto-categorisation)

An organisation builds up a **learned mapping** from supplier to the category it usually files them under, so future captures can be **auto-categorised**. Two halves:

- **Populate (in the DB).** A `plpgsql` trigger, `learn_supplier_category()` (`AFTER INSERT OR UPDATE ON expenses`, in `schema.sql`), upserts into `supplier_category_map`. The golden rule: it **only learns from CONFIRMED expenses** (`needs_review = FALSE`) — never from a Smart Upload skeleton/OCR-fill, whose category is still the placeholder, which would poison the dictionary. The key is **normalised** (`lower(btrim(supplier_name))`, so `Amazon`/`AMAZON`/`  amazon ` collapse), strategy is **last-write-wins** (one row per `(organisation_id, supplier_key)`), and a change-guard skips relearning on unrelated edits (e.g. an approval) so an old expense can't clobber a newer mapping. The table is **derived data** — safe to rebuild from `expenses`.
- **Consume (in Go).** On the OCR path, `OcrService.suggestCategory` looks up `GetSuggestedCategory` for the supplier that will land on the row, and `ApplySuggestedCategory` writes it onto the capture **inside the same fill transaction**. That UPDATE is SQL-guarded by `needs_review = TRUE`, so it only ever replaces the placeholder and **never overrides a category a human chose**. A miss leaves the placeholder. The manual-entry "suggest as you type" endpoint is deferred (see `BACKLOG.md`).

Like everything else, the dictionary is **organisation-scoped** (`organisation_id` leads the unique key and every lookup). Tested in `supplier_category_test.go`: the trigger directly against Postgres, and the auto-categorise loop end-to-end through the capture→OCR pipeline (Document AI faked).

### Claimant & on-behalf expense entry

An expense carries **two** user FKs: `user_id` is the **claimant** (whose expense it is) and `created_by_user_id` is who **recorded** it. Normally identical, but they diverge when an admin files for someone else:

- **On create**, an **owner/admin** may set `user_id` to another user (the optional `user_id` field on `CreateExpenseRequest`). `CreateExpense` authorises it: the caller must be owner/admin **and** the target must be an **active member of the same org** (checked via the org-scoped `GetMembership`, so a claimant from another tenant returns no rows → rejected); otherwise 403/422. `created_by_user_id` always stays the caller, preserving the audit of who actually entered it. The claimant is **not editable on update** — `UpdateExpense` never reads `user_id`. Covered by the "CREATE ON BEHALF" suite in `server_test.go`.
- **The picker.** The frontend's **Claimant** dropdown (expense form, directly above Category) is populated by **`GET /api/v1/members`** (`member_service.go`, `MemberService`) — **owner/admin only** (403 otherwise; returns members of all statuses, the UI filters to `active`). A non-owner/admin sees the dropdown **disabled and pinned to themselves**. The caller's role reaches the SPA via `organisation.role` in the login response (stored as `auth.isOrgAdmin`).

### Expense approval workflow (status state machine)

The `expenses.status` column moves through a small approval lifecycle, driven by **one endpoint** — `POST /api/v1/expenses/:id/status` with an `{"action": …}` discriminator — and a state machine that lives as **data** in `expense_status.go`:

```
            submit                approve
   DRAFT ───────────▶ SUBMITTED ───────────▶ APPROVED   (terminal; PAID is out of scope)
     ▲                    │
     │ reopen             │ reject
     └──────── REJECTED ◀─┘
```

| action  | from → to            | who                                | side-effects                                              |
|---------|----------------------|------------------------------------|-----------------------------------------------------------|
| submit  | DRAFT → SUBMITTED    | claimant (own) **or** owner/admin  | `submitted_at = now()`                                    |
| approve | SUBMITTED → APPROVED | **owner/admin only**               | `approved_at`, `approved_by_user_id` (keeps `submitted_at`) |
| reject  | SUBMITTED → REJECTED | **owner/admin only**               | `rejection_note` (reason required; keeps `submitted_at`)  |
| reopen  | REJECTED → DRAFT     | claimant (own) **or** owner/admin  | clears submitted/approved/rejection metadata (clean slate)|

Worth knowing:

- **SUBMITTED is a lock-in** (no withdraw to DRAFT), and **APPROVED is terminal** here. Fixing a rejection is two steps: **reopen** → edit → **submit** — which dovetails with the existing rule that DRAFT and REJECTED are the only **editable/deletable** states (`UpdateExpense`/`DeleteExpense`).
- **`status` ≠ `needs_review`.** This machine touches only the approval `status`; `needs_review` (the Smart-Upload data-capture axis) is orthogonal and untouched.
- **One dedicated SQL query per transition** (`SubmitExpense`/`ApproveExpense`/`RejectExpense`/`ReopenExpense`), each touching **only** the columns it owns. This is the key correctness point: the old single `UpdateExpenseStatus` overwrote every timestamp column on every call, so approving would have wiped `submitted_at`. Constants (`StatusDraft`…) replaced the magic strings, including in the editability checks.
- **Checks run inside one transaction** (load → authorise → state-check → write), mirroring `UpdateExpense`, to close the TOCTOU gap. Authorisation is admin-only for approve/reject, claimant-or-admin for submit/reopen. Validation is three-layered like contacts `charge_vat`: `oneof`/`required_if` binding (400) → service guards (422) → DB CHECK on `status`. Org-scoped throughout (cross-tenant → 404). Tested in `expense_status_test.go`.
- **Out of scope (see `BACKLOG.md`):** PAID transitions and audit logging (the `expense_audit_log` table is still unwired across *all* expense ops).

### Contacts module

A **contact** is a customer/supplier an organisation invoices or buys from (modelled on the FreeAgent "New Contact" screen). It is a **standalone domain**, structured exactly like `auth`: its own schema file (`db/schema/contacts_schema.sql`), its own queries (`db/queries/contacts.sql`), its own sqlc block generating package `db/contacts`, a service (`contact_service.go`) and handlers/DTOs/routes in `server.go` under `/api/v1/contacts` (list, create, get, update, soft-delete).

Worth knowing:

- **`organisation_name` ≠ `organisation_id`.** The contact's *company name* is `organisation_name`; the owning tenant is `organisation_id` (FK). Don't conflate them.
- **No money, but a units gotcha.** Contacts store no pence. The only numeric field, `default_payment_terms_days`, is a count of DAYS where **0 ("Due on Receipt") is distinct from NULL** ("no contact-level terms") — so the service uses the 0-preserving `pgInt32FromPtr` helper, NOT `pgNullInt32` (which maps 0→NULL).
- **`charge_vat`** is a `VARCHAR + CHECK` enum: `ALWAYS | NEVER | SAME_COUNTRY` (the form's three options; default `SAME_COUNTRY`). Validated three ways: `oneof` binding (400), the service guard (422), and the DB CHECK.
- **Names are permissive.** `first_name` / `last_name` / `organisation_name` are all nullable with no cross-column CHECK; the "must have a name or an org name" rule is deferred to the app layer (see `BACKLOG.md`).
- **Auth.** Any active member may create/read/list; **update/delete require the contact's creator or an owner/admin** (mirrors the expense ownership rule). Org-scoped + soft-deleted throughout. Tested in `contact_service_test.go`.

### Organisation / Company details

The **Company Details** screen (modelled on FreeAgent's) lives entirely on the existing `organisations` table — it is **not** a separate domain. Rather than a 1:1 `organisation_details` table, the missing fields were **added as nullable columns** to `organisations` (`db/schema/auth_schema.sql`), beside the company/tax fields already there (`legal_name`, `companies_house_number`, `utr`, `vrn`, `country_code`). New columns: `company_type` (CHECK enum: `limited|sole_trader|partnership|landlord|corporation`), a structured address (`address_line_1..3`, `town`, `region`, `postcode`), `paye_reference`, `accounts_office_reference`, `business_phone`, `contact_email`, `contact_phone`, `website`, `business_category`, `business_description`. The legacy free-text `registered_address` is **deprecated** (kept for back-compat, no longer written; see `BACKLOG.md`).

`OrganisationService` (`organisation_service.go`) is a thin layer over the existing `auth` queries (like `MemberService`), with handlers/DTOs/routes in `server.go` under **`/api/v1/organisation`** — a singleton resource (the org comes from the token, so there is no id in the path):

- **`GET`** returns the full company details — **any active member** may read.
- **`PUT`** updates them — **owner/admin only** (`isOrgAdmin`); reuses `GetOrganisation` / `UpdateOrganisation` (`db/queries/auth.sql`).
- **Field mapping.** The form's "Company name" → `name`, "Company Registration Number" → `companies_house_number`, "Corporation Tax Reference" → `utr`, "Country" → `country_code` (existing columns; `legal_name` is also exposed).
- **Read-modify-write.** PUT fetches the row first and passes through the columns this form does not own (`slug`, `native_currency`, `timezone`, and — until VAT is added to the form — `vrn`) so a save can't wipe them.
- **Validation.** `company_type` and `country_code` are checked three ways: `oneof` / `len` binding (400), the service guards `normaliseCompanyType` / `normaliseCountryCode` (422), and the DB CHECK. Tested in `organisation_service_test.go`.

### My Details (user profile)

The **My Details** screen (modelled on FreeAgent's) is the per-user counterpart to Company Details: the signed-in user edits their **own** profile (`first_name` / `last_name`), with a read-only **login email** and a display of their **Mailgun receipt-inbox address**. Like the organisation, it lives entirely on an existing table (`users`) — **no schema change, no new sqlc** (the `GetUser` / `UpdateUser` queries already existed).

`UserService` (`user_service.go`) is a thin layer over the existing `auth` queries (like `OrganisationService` / `MemberService`), with handlers/DTOs/routes in `server.go` under **`/api/v1/profile`** — a singleton resource (the user comes from the token, so there is no id in the path):

- **`GET`** returns the caller's own profile. **`PUT`** updates `first_name` / `last_name`.
- **No role check — and that's the point.** Unlike Company Details (owner/admin to edit), there is no membership/authorize step: the target is always `authUserID` from the token, so a caller can only ever read/edit **themselves**. Multi-tenant isolation is inherent (there is no id to pass).
- **Read-modify-write.** `PUT` fetches the row first and passes through the columns this form doesn't own (`phone`, `avatar_url`) so a save can't wipe them — the same preservation pattern as the org PUT (`slug`/`vrn`/…). The login `email` is not editable here at all.
- **Reuse.** The GET/PUT responses reuse the login `userResponse` + `newUserResponse` projector (`auth_handler.go`) — the same safe shape login returns.
- **Validation.** `first_name` / `last_name` are checked two ways: `required,max=100` binding (400) and a service trim-and-reject guard (422, e.g. a whitespace-only name). The Mailgun address shown on the page comes from the existing `GET /api/v1/inbox-address`. Tested in `user_service_test.go`. Frontend: `MyDetailsView.vue` (mirrors `CompanyDetailsView.vue`), reached from the account dropdown's **My Details** item (directly under **Company Details**); a save also calls the auth store's `patchUser` so the top-bar name updates immediately.

### Email-to-expense (Mailgun inbound)

Users forward receipt files to a **dedicated email address per (user, organisation)** and each becomes a **draft expense**. It is an ingestion channel onto the existing Smart-Upload pipeline — not a new expense path.

- **Push, and our DB is the system of record.** Mailgun receives the mail and **POSTs it (parsed) to `POST /api/v1/webhooks/mailgun/inbound`** (`email_inbox_handler.go`) — as `multipart/form-data` when the email has attachments, otherwise `application/x-www-form-urlencoded`; the handler reads fields via `c.PostForm` so **both encodings work** (an attachment-less HTML-body email is accepted, not 400'd). We read the `To`/files straight from the payload and persist to **our** Postgres + GCS; Mailgun's message store is never read back. The webhook is **public** but **HMAC-verified** (`verifyMailgunSignature`, `MAILGUN_INBOUND_SIGNING_KEY`) — it carries no bearer token. **Persist-then-ack:** it returns 200 only after a durable write, a transient failure returns 500 so Mailgun retries, and the **Message-Id dedupe** (the `email_inbox` domain's `inbound_email_events`) makes that retry safe (a re-delivery of a finished email is skipped; a half-done one is reprocessed). Beyond Message-Id (which only catches one delivery's retries), a **content hash** (SHA-256, stored per attachment as `expense_attachments.content_hash`, computed via `io.TeeReader` during the GCS upload) dedupes the *same receipt re-sent as a new email*: an attachment whose hash already matches a non-deleted expense for that claimant is skipped, and an email whose attachments were all such duplicates finishes `ignored_duplicate`.
- **Addressing: `{name}.{org-slug}@INBOX_DOMAIN`** (e.g. `aydin.gunal.acme-ltd@…`), one per membership, stored as `inbox_local_part` on `organisation_memberships` (UNIQUE, generated lazily, **read-only** via `GET /api/v1/inbox-address`). It is **human-readable, not a secret**, so authorisation is the **sender check**: the `From` must be an **active member of the address's org** (`senderIsActiveMember`) — which rejects cross-tenant and external senders. The address identifies the **claimant**; v1 sets `created_by` = claimant too and records the true submitter in `inbound_email_events.sender`.
- **Capture.** **Inline body images** (logos/signatures referenced via `cid:`, which Mailgun delivers as `attachment-N` parts and lists in `content-id-map`) are **filtered out** (`inlineAttachmentNames`) so only genuine attachments are captured — otherwise a sender's logo would become a draft instead of the real receipt. Each remaining attachment goes through the existing `AttachmentService.CaptureFromReceipt` with `document_type="receipt"` (**always** the Expense parser — auto-detect is deferred). A bad-MIME file is skipped (others still captured). When there's **no usable attachment**, the **HTML body is rendered to a PDF** (`HTMLRenderer` → Gotenberg, `GOTENBERG_URL`; optional like GCS/DocAI) and captured as `email-body.pdf`.
- **Module shape.** The `email_inbox` domain mirrors the others (`db/schema/email_inbox_schema.sql`, `db/queries/email_inbox.sql`, its own sqlc block → `db/email_inbox`) and holds only the inbound-email event log; the inbox **address** itself is an `auth`-domain concern (a column on `organisation_memberships`). Tested in `email_inbox_test.go` (real Postgres + GCS; Mailgun + Gotenberg faked).

### FreeAgent expense push (external GCP-native integration)

When an expense is **approved**, it is pushed to the organisation's FreeAgent account — but the **integration logic lives OUTSIDE the monolith**, in a **Cloud Workflow**, so the source→destination field mapping is config (YAML), not Go, and future providers (Xero, …) are sibling workflows. The monolith's whole role is: emit an event, hold OAuth credentials/tokens, serve the expense data, and record the outcome. It knows **nothing** about how to build a FreeAgent expense.

- **Trigger = Pub/Sub.** On a successful `approve` transition, `ExpenseService` publishes an `expense.approved` event (IDs only) to a Pub/Sub topic (`events.go` / `events_pubsub.go`, pubsub **v2**; optional + nil-guarded like GCS via `PUBSUB_EXPENSE_APPROVED_TOPIC`). **Best-effort:** a publish failure does NOT undo the committed approval — it's logged, and recoverable via the manual re-push. Eventarc routes the topic to the **Cloud Workflow** `deploy/workflows/freeagent-push.yaml` (the externalised mapping: claimant→FreeAgent-user by email, category→nominal-code URL, `ec_status` remap, and the **gross-value sign negation** FreeAgent expects).
- **OAuth is the monolith's job** (interactive, one-time per org). `freeagent_client.go` is an **auth-only** FreeAgent client (`ExchangeCode`/`RefreshToken`/`authorizeURL`); `integration_service.go` (`IntegrationService`) runs the connect flow — save credentials, build the authorize URL (signed-`state` CSRF via the existing PASETO `tokenMaker`), handle the public callback, store/refresh tokens, status, disconnect. Handlers + DTOs in `integration_handler.go`, routes in `server.go` under `/api/v1/integrations/freeagent` (settings, owner/admin) and `/api/v1/freeagent/{connect,callback}` (connect returns the authorize URL as **JSON** so the bearer-token SPA navigates itself; callback is **public** and 302s back to the SPA). Access tokens last ~1h (refreshed server-side ~5 min early); refresh tokens ~20y.
- **The workflow calls back via OIDC-gated `/internal/v1` endpoints** (`integration_internal.go` + `integration_internal_handler.go`): a token-vend (refreshing if near expiry, clearing the connection on a failed refresh → "needs reconnect"), the provider-neutral expense data (**money converted to decimal strings in Go** via `money.MinorToPounds` — never float, never in YAML; `ec_status` stays raw), and a push-result sink (idempotency: the workflow skips an `already_pushed` expense). The middleware `requireWorkflowOIDC` (`google.golang.org/api/idtoken`) accepts only a Google-signed token for `WORKFLOW_SERVICE_ACCOUNT` (fails closed when unset) — the inverse of the outbound OIDC in `html_renderer.go`, same shape as the public-but-verified Mailgun webhook.
- **Manual re-push.** `POST /api/v1/integrations/freeagent/expenses/:id/push` (owner/admin) re-emits the event for an APPROVED expense; the workflow's `already_pushed` guard makes it idempotent. `ExpenseService.RepublishApprovedExpense`.
- **Module shape.** The `integrations` DB domain (`db/schema/integrations_schema.sql`, `db/queries/integrations.sql`, sqlc block → `db/integrations`) has two tables: `organisation_integrations` (per-(org,provider) credentials + tokens) and `integration_expense_pushes` (the outcome/idempotency ledger). The Go code is `package main` root files (matching `storage_gcs.go` etc.), NOT a sub-package. New env: `PUBSUB_EXPENSE_APPROVED_TOPIC`, `GOOGLE_CLOUD_PROJECT`, `WORKFLOW_SERVICE_ACCOUNT`, `API_PUBLIC_URL` (the BACKEND's own URL, for the OAuth redirect — distinct from the frontend `APP_BASE_URL`), `FREEAGENT_SANDBOX`. Provisioning + the data-residency note (infra runs in `europe-west1` = EU, not UK) are in `deploy/README.md`. Tested in `integration_service_test.go`, `integration_internal_test.go`, `events_test.go` (real Postgres; FreeAgent + Pub/Sub faked; OIDC's positive path validated at deploy). Token encryption at rest + a transactional outbox are deferred — see `BACKLOG.md`.

> Update this section whenever the structure changes meaningfully — it should always reflect reality.

## Database & sqlc Workflow

1. **Schema** lives as full DDL in `db/schema/` (`schema.sql` for expenses, `auth_schema.sql` for auth) — the source of truth, **not** incremental migration files. Design it deliberately (types, constraints, foreign keys, indexes) before writing queries.
2. **Queries** are annotated `.sql` files in `db/queries/`, **one file per domain** (`query.sql` for expenses, `auth.sql` for auth).
3. **Generated Go code** is emitted **per domain** into its own package: `db/expenses/` (package `expenses`) and `db/auth/` (package `auth`). `sqlc.yaml` has a **separate generation block for each** — adding a new domain means adding a new block (its own `queries`, `out`, `package`).
4. **Generate** after any schema or query change:
   ```
   sqlc generate
   ```
5. **Never hand-edit generated files** (anything under `db/expenses/` or `db/auth/`, marked `// Code generated by sqlc. DO NOT EDIT.`) — fix the `.sql` source and regenerate.
6. Use **pgx/v5** as the underlying driver/connection pool (`pgxpool`).

> sqlc detail worth knowing: the **auth** generation block lists *both* schema files (`db/schema/schema.sql` **and** `db/schema/auth_schema.sql`), because `auth_schema.sql` references the `expenses` table and the `set_updated_at()` function defined in `schema.sql`. It also sets `omit_unused_structs: true` so the expenses models aren't duplicated into the `auth` package.

### Conventions
- Money columns: `bigint`, representing minor units (pence). Never `numeric`/`float` for currency amounts that participate in arithmetic.
- Every table should have sensible `created_at` / `updated_at` timestamps and, where multi-tenant, an `organisation_id` foreign key.
- Prefer explicit column lists in queries over `SELECT *`.
- New tables/columns should include a short comment in the migration explaining *why* they exist, especially if driven by a compliance requirement (GDPR/MTD/FCA).

## Direct Database Access (terminal)

Claude Code **is authorized to query the development database directly — reads and writes — without asking each time** (confirmed by Aydin). Use it to inspect schema/data, verify changes, and debug while building.

- **Connection string:** read it from `.env` as `DATABASE_URL` — that file is the source of truth (currently the `accounting` database on a remote shared dev Postgres). Don't hardcode or echo the password.
- **`psql` is installed via Homebrew `libpq`, which is keg-only**, so bare `psql` is NOT on `PATH` in a non-interactive shell. Use the full path:
  ```
  /opt/homebrew/opt/libpq/bin/psql
  ```
- **Recommended invocation** (pulls the URL from `.env`, so the password never lands in the command/logs):
  ```bash
  /opt/homebrew/opt/libpq/bin/psql "$(grep -E '^DATABASE_URL=' .env | cut -d= -f2-)" -c "SELECT ..."
  ```

Cautions:
- This is a **shared dev database**, not production — and the integration tests (`go test ./...`) read and write to it. **Avoid destructive operations (`DROP`/`TRUNCATE`/bulk `DELETE`) unless explicitly asked.**
- Schema and seed are already applied (expenses + auth tables exist). The seeded dev login user is `dev@example.com` (org `00000000-0000-0000-0000-000000000001`).

## Transactions & Error Handling

- Use the existing **transaction wrapper pattern** for any operation that writes to more than one table or needs atomicity. New service methods that mutate data should use this wrapper rather than calling the pool directly.
- Use the existing **`AppError`** type with `ErrorCode` constants for all error returns from service/repository layers. Handlers translate `AppError` into HTTP responses (status code + JSON body) — don't leak raw `pgx`/`sql` errors to the HTTP layer.
- When adding a new error case, add a new `ErrorCode` constant rather than reusing an unrelated one or returning a bare `errors.New`.
- **In handlers, don't hand-roll the error envelope.** Translate every service error with `respondError(c, err)` (`handler_helpers.go`) — it derives the HTTP status from the `AppError`, writes the standard `{"error":{code,message}}` body, and logs 500s via `slog`. Bind request bodies with `if !bindJSON(c, &req) { return }`, which emits a standard 400 (`bad_request`) on a malformed body. (The Mailgun webhook is the one deliberate exception: it maps errors to retry-friendly statuses itself.)
- **One shared membership check.** Services authorise via `authorizeMember` (`authz.go`); each service's `authorize()` is a thin delegate — don't re-implement the GetMembership/active/role logic.

## Auth & Multi-tenancy

- Currently using a **stub `organisation_id`** as a placeholder. This will be replaced by real JWT auth middleware.
- When working on auth: this is a deliberate, explicit decision point (Authboss vs Ory Kratos vs a service-based approach). **Don't silently introduce an auth library** — flag it and discuss before adding a dependency of this size.
- Every query/handler that touches tenant data must scope by `organisation_id`. When adding new tables/queries, double-check this scoping is present — it's a security-critical pattern, not an afterthought.

## Testing

Testing is a first-class part of this project, not an optional extra.

- **Use real integration tests against PostgreSQL**, not mocks, for repository/service-layer logic. Mocks are acceptable only for external third-party services (e.g., TrueLayer) where hitting the real API in tests isn't practical.
- **Test data:** read from seeded fixtures rather than inserting throwaway rows ad hoc, following the existing test helper pattern. Add new seed data alongside new tables/features as needed.
- **Every new module/feature should ship with tests covering:**
  - Happy path
  - Validation/error cases (mapped to the correct `ErrorCode`)
  - Money/decimal conversion correctness (no float drift, correct rounding)
  - Multi-tenant scoping (a query for org A never returns org B's data)
- Run tests with:
  ```
  go test ./...
  ```
- For DB-backed tests, ensure Postgres is reachable via `DATABASE_URL` (in `.env`) and the DDL in `db/schema/` has been applied. Document any setup steps you add (e.g., a `docker-compose.test.yml` or a `make test` target) so they're reproducible.
- The **attachment tests hit the real GCS dev bucket** (no emulator/fake), mirroring how the DB tests hit real Postgres. They require `GCS_BUCKET` set in `.env` plus GCP credentials that can read/write it (`gcloud auth application-default login`, or `GOOGLE_APPLICATION_CREDENTIALS`); the signed-URL test additionally needs a service-account *signer*. When `GCS_BUCKET` is unset they **skip** (so `go test ./...` still passes without GCS), exactly like DB tests skip without `DATABASE_URL`. Each test uses a unique per-expense key prefix and deletes its objects in `t.Cleanup` so the shared bucket stays clean.

## Architecture & Methodology Principles

These are the guiding principles for this codebase. Apply them pragmatically — this is a small, evolving project, not a reference architecture for its own sake.

1. **Layered separation, kept simple:** handler (HTTP/Gin) → service (business logic) → repository (sqlc-generated DB calls). Each layer should be testable in isolation where practical, but don't over-abstract — a thin service layer that mostly delegates is fine for now.
2. **Domain modules over technical layers at the top level.** Organize primarily by business domain (expenses, invoices, contacts) rather than by technical type (all models in one folder, all handlers in another).
3. **Explicit over implicit.** Avoid magic (reflection-heavy ORMs, hidden global state, implicit type conversions for money). sqlc is chosen specifically because it keeps SQL visible and Go types explicit.
4. **Financial correctness is non-negotiable:**
   - Integers in minor units in the DB and in internal calculations.
   - `shopspring/decimal` for any conversion/display logic.
   - Round explicitly and document the rounding rule wherever it matters (VAT, totals).
5. **Database integrity does real work.** Use PostgreSQL constraints (NOT NULL, foreign keys, CHECK constraints, unique constraints) as a defense layer — don't rely on application code alone to enforce invariants.
6. **Incremental, reversible change.** Prefer additive migrations. Avoid destructive schema changes once there's real data; write migrations that can be rolled back.
7. **Boundaries for future extraction.** Code is organized as a monolith now, but domain modules should have clear internal boundaries (avoid deep cross-module coupling) so performance-critical pieces (e.g., a future ledger engine) could be extracted into a separate Go service later without a rewrite.
8. **Document decisions, not just code.** Significant architectural decisions (auth library choice, module boundaries, schema design rationale) should be captured in the project's living decision doc, not just in commit messages.

## Working Conventions for Claude Code

- **Before writing code for a new feature/module:** briefly restate the plan (files to add/change, schema impact, new dependencies) and check it against the principles above.
- **When adding a dependency:** explain what it's for and why it's needed — don't add libraries casually, especially for auth, security, or anything touching money.
- **When generating sqlc queries:** show the `.sql` query alongside an explanation of what it does and any indexing implications.
- **When touching money/financial logic:** explicitly call out rounding behavior and units (pence vs pounds) in comments.
- **After implementing a feature:** propose the corresponding tests (or write them) — don't treat tests as a follow-up "if there's time."
- **Keep commits/changes scoped** to the feature or module being discussed. Flag (but don't make) unrelated improvements you notice.
- **Ask before introducing new architectural patterns** (new middleware style, new package layout convention, etc.) — consistency matters more than local optimization in a learning-first codebase.
- **Track deferred work in [BACKLOG.md](BACKLOG.md).** When you intentionally defer something (or notice a TODO worth not losing), add it there instead of relying on commit messages or chat. Remove items as they're done.
