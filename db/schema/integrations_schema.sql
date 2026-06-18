-- =============================================================================
-- INTEGRATIONS MODULE — DATABASE SCHEMA
-- Accounting SaaS Platform (UK-focused, HMRC MTD-ready)
-- PostgreSQL 15+
--
-- This domain holds the state for pushing our data OUT to third-party accounting
-- systems (FreeAgent first; Xero/QuickBooks later). The actual field mapping and
-- the outbound API calls do NOT live here or anywhere in the Go monolith — they
-- run in an external Cloud Workflow. The monolith only:
--   1. stores each org's per-provider OAuth credentials + live tokens, and
--   2. records the outcome of each push (so retries are idempotent).
--
-- Design decisions:
--
--   ONE GENERIC DOMAIN, NOT ONE TABLE PER PROVIDER.
--   `provider` is a free-text key ('freeagent', 'xero', …) rather than a CHECK
--   enum, so onboarding a new integration is a new ROW, never a schema change.
--   These tables are deliberately provider-agnostic.
--
--   TOKENS LIVE HERE, MAPPING DOES NOT.
--   organisation_integrations holds the admin-entered client_id/client_secret
--   AND the access/refresh tokens obtained during the one-time OAuth connect.
--   The monolith refreshes the short-lived access token server-side; the tokens
--   never leave Postgres. How to translate an expense into a FreeAgent payload is
--   the Cloud Workflow's job, not a column here.
--
--   PUSH OUTCOME IS DERIVED/IDEMPOTENCY STATE.
--   integration_expense_pushes is one row per (integration, expense). It is what
--   makes the push idempotent: a successful row carries the external URL, so a
--   re-delivered event (Eventarc retry or a manual re-push) is skipped. It is
--   safe to rebuild from the external system if ever needed.
--
--   MULTI-TENANCY.
--   organisation_integrations.organisation_id scopes every row; the unique
--   (organisation_id, provider) key both enforces "one connection per provider
--   per org" and backs the GetIntegration lookup. integration_expense_pushes
--   reaches the tenant via its integration_id FK.
--
--   APPLIED AFTER schema.sql + auth_schema.sql, so set_updated_at() (schema.sql),
--   the expenses table (schema.sql) and the organisations table (auth_schema.sql)
--   all already exist — the foreign keys below are declared INLINE.
-- =============================================================================


-- -----------------------------------------------------------------------------
-- organisation_integrations
-- One row per (organisation, provider). Created when an admin saves the app
-- credentials; the token columns fill in after they complete the OAuth connect.
-- -----------------------------------------------------------------------------
CREATE TABLE organisation_integrations (
    id               UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    organisation_id  UUID        NOT NULL REFERENCES organisations(id) ON DELETE CASCADE,

    -- Provider key, e.g. 'freeagent'. Free text (no CHECK) so adding a provider
    -- is a new row, not a migration.
    provider         VARCHAR(50) NOT NULL,

    -- OAuth APP credentials the org admin enters (from the provider's developer
    -- dashboard). They identify OUR app, not any user's data.
    -- TODO: encrypt at rest before production (same note as organisations.mtd_*).
    client_id        TEXT,
    client_secret    TEXT,

    -- Live OAuth tokens, stored after the one-time interactive connect.
    -- access_token is short-lived (~1h) and refreshed server-side using
    -- refresh_token; token_expires_at drives the "refresh if near expiry" check.
    access_token     TEXT,
    refresh_token    TEXT,
    token_expires_at TIMESTAMPTZ,

    -- NULL until the org completes the connect flow (credentials saved but not
    -- yet authorised). A failed refresh clears this back to NULL → "needs reconnect".
    connected_at     TIMESTAMPTZ,

    created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at       TIMESTAMPTZ NOT NULL DEFAULT now(),

    -- One connection per provider per org. Also the index behind GetIntegration.
    CONSTRAINT uq_org_provider UNIQUE (organisation_id, provider)
);


-- -----------------------------------------------------------------------------
-- integration_expense_pushes
-- The outcome ledger: one row per (integration, expense). Written by the
-- workflow (via the monolith's internal push-result endpoint) and read back to
-- compute `already_pushed`. This is the idempotency anchor for the whole flow.
-- -----------------------------------------------------------------------------
CREATE TABLE integration_expense_pushes (
    id                   UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    integration_id       UUID        NOT NULL REFERENCES organisation_integrations(id) ON DELETE CASCADE,
    expense_id           UUID        NOT NULL REFERENCES expenses(id) ON DELETE CASCADE,

    external_expense_ref TEXT,       -- the external system's expense URL on success; NULL on failure
    push_error           TEXT,       -- last failure message; NULL on success

    pushed_at            TIMESTAMPTZ NOT NULL DEFAULT now(),

    -- One result row per (integration, expense); UPSERTs target this key, so a
    -- retry updates the same row instead of duplicating. Also backs GetExpensePushResult.
    CONSTRAINT uq_integration_expense UNIQUE (integration_id, expense_id)
);


-- =============================================================================
-- TRIGGERS — auto-update updated_at
-- Reuses set_updated_at() from db/schema/schema.sql (like contacts/auth do).
-- integration_expense_pushes has no updated_at (only pushed_at), so it needs no
-- trigger.
-- =============================================================================
CREATE TRIGGER trg_organisation_integrations_updated_at
    BEFORE UPDATE ON organisation_integrations
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();


-- =============================================================================
-- COMMENTS
-- =============================================================================
COMMENT ON TABLE organisation_integrations IS 'Per-(org,provider) third-party accounting connection: admin-entered OAuth app credentials + live access/refresh tokens. Provider is free text so new integrations need no schema change.';
COMMENT ON COLUMN organisation_integrations.connected_at IS 'NULL = credentials saved but not yet authorised, or a refresh failed (needs reconnect). Set on a successful OAuth connect.';
COMMENT ON TABLE integration_expense_pushes IS 'Outcome ledger: one row per (integration, expense). external_expense_ref on success / push_error on failure. The unique (integration_id, expense_id) key makes pushes idempotent across Eventarc retries and manual re-pushes.';
