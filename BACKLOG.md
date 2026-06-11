# Backlog / Deferred Items

A running list of work that was intentionally deferred, plus notable TODOs found
in the code. Add to this file whenever you defer something so it isn't lost in a
commit message or chat. Remove items as they're completed.

_Last updated: 2026-06-11_

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

- **List filtering & pagination for `GET /api/v1/expenses`.** Today the endpoint returns the full set the caller may see (owner/admin: whole org; others: own) with no filters or paging. Add query-param filtering (date range, status, project) and pagination (limit/offset or cursor), preserving the existing owner/admin-vs-own scoping in `ListExpenses`. Reuse the existing org-scoped sqlc queries `ListExpensesByDateRange` / `ListExpensesByStatus` / `ListExpensesByProject` (`db/queries/query.sql`); user-scoped + filtered variants would need new queries. _Files: `expense_service.go`, `server.go`, `db/queries/query.sql`._


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

- **VAT computation.** `CreateExpense` stores zero VAT regardless of
  `vat_rate_id`; it should look up the rate from `vat_rates` and compute the
  amounts. _File: `expense_service.go` (Step 3)._
- **Expense audit log on create.** The create transaction has a placeholder for
  an audit-log INSERT (`CreateAuditEntry` query + call) that isn't implemented.
  _File: `expense_service.go` (`withTransaction`)._
- **Structured logging.** Handlers carry `_ = appErr.Error()` placeholders for a
  real logger (slog/zap) instead of `log`/`fmt`. _Files: `server.go`,
  `auth_handler.go`._
- **Encrypt MTD OAuth tokens at rest.** `organisations.mtd_access_token` /
  `mtd_refresh_token` are stored in plaintext; the schema flags encrypting them
  before production. _File: `db/schema/auth_schema.sql`._
