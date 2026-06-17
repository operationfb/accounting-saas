-- email_inbox_schema.sql
-- =============================================================================
-- EMAIL-TO-EXPENSE — inbound email audit + idempotency
--
-- This is the data layer for the "forward a receipt by email" channel. Mailgun
-- receives mail for the inbox domain and POSTs each message (parsed, with the
-- attachment bytes inline) to our webhook; we turn the attachments into draft
-- expenses. The actual receipt FILES and DRAFTS are stored by the existing
-- attachment/expense tables — this file adds ONE small table whose jobs are:
--
--   1. IDEMPOTENCY. Mailgun retries the webhook (for hours) until we return 2xx,
--      so the same email can arrive more than once. We record each email's
--      Message-Id with a UNIQUE constraint and skip anything we've already seen,
--      so a retry never creates a second draft.
--
--   2. AUDIT. A GDPR-relevant record of what arrived, from whom, where it was
--      routed, and what we did with it (processed / ignored / error).
--
-- This table is DERIVED, low-value data — it can be truncated without affecting
-- expenses. A retention/purge policy for the PII it holds (sender/recipient/
-- subject) is deferred (see BACKLOG.md).
-- =============================================================================

CREATE TABLE inbound_email_events (
    id                  UUID        PRIMARY KEY DEFAULT gen_random_uuid(),

    -- The email's 'Message-Id' header — our dedupe key. UNIQUE is what makes the
    -- whole pipeline idempotent: a retried delivery hits this constraint and is
    -- skipped. (We claim the row first, then process, so concurrent retries can't
    -- both proceed.)
    provider_message_id TEXT        NOT NULL UNIQUE,

    -- The organisation the email was routed to. NULL until we resolve the
    -- recipient address to a (user, org); a message to an unknown address is
    -- recorded with org NULL and status 'ignored_unknown_address'.
    organisation_id     UUID        REFERENCES organisations(id),

    -- The inbox address it was delivered to (Mailgun's envelope recipient — more
    -- reliable than the To header for catch-all delivery, e.g. Bcc).
    recipient           TEXT        NOT NULL,

    -- The From-header email. This is the TRUE submitter (which may differ from the
    -- inbox owner when a colleague forwards on someone's behalf); kept for audit.
    sender              TEXT        NOT NULL,

    subject             TEXT,

    -- What happened to this email. 'received' is the initial claim state; the
    -- webhook updates it to a terminal value once processing finishes.
    status              VARCHAR(40) NOT NULL DEFAULT 'received'
                        CHECK (status IN (
                            'received',                    -- claimed, not yet finished
                            'processed',                   -- one or more drafts created
                            'ignored_unknown_address',     -- recipient didn't resolve to a member
                            'ignored_sender_not_member',   -- sender isn't an active member of the org
                            'ignored_no_attachments',      -- nothing capturable (no file, no renderable body)
                            'ignored_duplicate',           -- every attachment was an exact duplicate of an existing draft
                            'error'                        -- unexpected failure
                        )),

    -- How many files (and rendered HTML bodies) the email carried, and how many
    -- drafts we actually created from it — handy for support/debugging.
    attachment_count    INTEGER     NOT NULL DEFAULT 0,
    drafts_created      INTEGER     NOT NULL DEFAULT 0,

    -- Free-text note, e.g. "skipped attachment 2: unsupported type text/plain".
    note                TEXT,

    created_at          TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Listing/auditing an organisation's recent inbound emails (newest first).
CREATE INDEX idx_inbound_email_events_org
    ON inbound_email_events (organisation_id, created_at DESC)
    WHERE organisation_id IS NOT NULL;
