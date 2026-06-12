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
├── server.go            # Gin engine + Server struct + route registration + expense handlers
├── auth_handler.go      # AuthHandler: POST /api/v1/auth/login → PASETO token + sanitised user
├── expense_service.go   # Expense business logic (validation, money conversion, DB orchestration)
├── errors.go            # AppError type + ErrorCode constants + handler→HTTP mapping
├── server_test.go       # Integration tests (real Postgres) for the HTTP handlers
├── sqlc.yaml            # sqlc config — one generation block PER domain (expenses, auth)
│
├── db/
│   ├── schema/          # Source-of-truth DDL (full CREATE TABLE files, not migrations)
│   │   ├── schema.sql        # expenses module + the set_updated_at() trigger function
│   │   └── auth_schema.sql   # auth module: users, organisations, organisation_memberships
│   ├── queries/         # Annotated SQL = sqlc input (one file per domain)
│   │   ├── query.sql         # expenses queries
│   │   └── auth.sql          # auth queries (CreateUser, GetUserByEmail, memberships, ...)
│   ├── seeds/           # Reproducible seed data (e.g. expense_categories.sql)
│   ├── expenses/        # GENERATED (package expenses) — never hand-edit
│   └── auth/            # GENERATED (package auth) — never hand-edit
│
├── token/              # PASETO authentication tokens
│   ├── maker.go              # Maker interface (CreateToken / VerifyToken)
│   ├── paseto_maker.go       # PasetoMaker implementation (PASETO v2 local)
│   └── payload.go            # Token Payload (UserID, OrganisationID, expiry)
│
└── util/
    └── random.go        # Random test-data helpers for the integration tests
```

Config/tooling at the root: `go.mod` / `go.sum` (deps), `.env` (local config — `DATABASE_URL`, `PASETO_SYMMETRIC_KEY`, `PORT`), `.gitignore`, and this `CLAUDE.md`.

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
