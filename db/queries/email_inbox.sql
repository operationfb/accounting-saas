-- email_inbox.sql
-- =============================================================================
-- Queries for the inbound-email audit/idempotency log (inbound_email_events).
-- See db/schema/email_inbox_schema.sql for the table and the why.
--
-- The two queries implement "claim-first, then finish":
--   1. ClaimInboundEmailEvent inserts the event keyed by Message-Id. The UNIQUE
--      constraint means a RETRY of an email we've already handled inserts
--      nothing and returns no row — that's our idempotency signal.
--   2. FinishInboundEmailEvent records the outcome once processing completes.
-- =============================================================================

-- -----------------------------------------------------------------------------
-- ClaimInboundEmailEvent
-- Atomically claims an inbound email for processing, keyed by its Message-Id.
-- ON CONFLICT DO NOTHING + RETURNING id means: a NEW email returns its new id; a
-- DUPLICATE (already seen) returns no row (pgx.ErrNoRows), which the caller treats
-- as "skip, already processed". Concurrent retries can't both win the insert.
-- -----------------------------------------------------------------------------
-- name: ClaimInboundEmailEvent :one
INSERT INTO inbound_email_events (provider_message_id, recipient, sender, subject)
VALUES ($1, $2, $3, $4)
ON CONFLICT (provider_message_id) DO NOTHING
RETURNING id;

-- -----------------------------------------------------------------------------
-- FinishInboundEmailEvent
-- Records the terminal outcome of a claimed event: the status, the organisation
-- it resolved to (NULL for an unknown address), how many attachments it carried,
-- how many drafts we created, and an optional note.
-- -----------------------------------------------------------------------------
-- name: FinishInboundEmailEvent :exec
UPDATE inbound_email_events SET
    status           = $2,
    organisation_id  = $3,
    attachment_count = $4,
    drafts_created   = $5,
    note             = $6
WHERE id = $1;

-- -----------------------------------------------------------------------------
-- GetInboundEmailEventByMessageID
-- Looks up an already-claimed event by Message-Id. Used when ClaimInboundEmailEvent
-- hits a conflict (the email's been seen before): if the prior attempt reached a
-- TERMINAL status we skip it (a genuine duplicate delivery); if it's still
-- 'received'/'error' (a prior attempt that failed mid-way, e.g. a transient
-- storage error before we acked) we reprocess it, so a Mailgun retry actually
-- retries the work rather than being silently dropped.
-- -----------------------------------------------------------------------------
-- name: GetInboundEmailEventByMessageID :one
SELECT id, status FROM inbound_email_events
WHERE provider_message_id = $1;
