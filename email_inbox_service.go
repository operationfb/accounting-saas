package main

// email_inbox_service.go
// =============================================================================
// EmailInboxService — the business logic for the email-to-expense channel.
//
// Two jobs:
//   1. Ingest()                — turn one received email into draft expense(s),
//                                reusing the existing Smart Upload capture
//                                pipeline (AttachmentService.CaptureFromReceipt).
//   2. GetOrCreateInboxAddress — the human-readable, per-(user,org) address a
//                                user forwards receipts to (generated lazily,
//                                displayed read-only).
//
// Routing & auth (see CLAUDE.md / the plan):
//   - The recipient address identifies the CLAIMANT (whose expense it is).
//   - The sender (From) must be an ACTIVE MEMBER of that organisation — the only
//     gate, because the address is human-readable, not a secret.
//   - Everything persists to OUR Postgres + GCS; Mailgun is just transport. We
//     dedupe on Message-Id so a retried webhook never creates a second draft,
//     while a webhook that 500'd mid-way is reprocessed on Mailgun's retry.
// =============================================================================

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"html"
	"io"
	"log"
	"net/mail"
	"strings"
	"time"
	"unicode"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"golang.org/x/text/runes"
	"golang.org/x/text/transform"
	"golang.org/x/text/unicode/norm"

	auth "github.com/operationfb/accounting-saas/db/auth"
	emailinbox "github.com/operationfb/accounting-saas/db/email_inbox"
	htmlrender "github.com/operationfb/accounting-saas/internal/htmlrender"
	ocr "github.com/operationfb/accounting-saas/internal/ocr"
)

// emailDocumentType is the OCR routing for every emailed file. Per the product
// decision, email captures always use the receipt (Expense) parser; the user can
// correct on the review screen. (Auto-detect is a backlog item.)
const emailDocumentType = ocr.DocumentTypeReceipt

// maxInboxLocalPartAttempts bounds the disambiguation loop when generating an
// address. name+org-slug collisions are very rare; this just prevents an
// unbounded loop in a pathological case.
const maxInboxLocalPartAttempts = 20

// defaultProcessTimeout bounds the BACKGROUND processing of one email (the
// capture/GCS/render work the webhook hands off after acking). Generous headroom
// over the seconds a couple of GCS uploads + a Gotenberg render take; mirrors
// internal/ocr's defaultOCRTimeout.
const defaultProcessTimeout = 2 * time.Minute

// EmailInboxService wires the inbound-email channel to the capture pipeline.
type EmailInboxService struct {
	authQueries  auth.Querier
	eventQueries *emailinbox.Queries
	attachments  *AttachmentService
	renderer     htmlrender.Renderer // nil when GOTENBERG_URL is unset → HTML-body capture is skipped
	inboxDomain  string              // e.g. "receipts.example.com" (lower-cased)

	// runInBackground runs the post-claim processing off the request path so the
	// webhook can ack Mailgun fast (see Accept). Defaults to spawning a goroutine;
	// tests swap in an inline runner so they stay deterministic.
	runInBackground func(func())
	// processTimeout bounds one background processing run (see defaultProcessTimeout).
	processTimeout time.Duration
}

// NewEmailInboxService constructs the service. renderer may be nil (HTML-body
// capture then disabled). inboxDomain is required for the channel to do anything.
func NewEmailInboxService(authQueries auth.Querier, eventQueries *emailinbox.Queries, attachments *AttachmentService, renderer htmlrender.Renderer, inboxDomain string) *EmailInboxService {
	return &EmailInboxService{
		authQueries:     authQueries,
		eventQueries:    eventQueries,
		attachments:     attachments,
		renderer:        renderer,
		inboxDomain:     strings.ToLower(strings.TrimSpace(inboxDomain)),
		runInBackground: func(fn func()) { go fn() },
		processTimeout:  defaultProcessTimeout,
	}
}

// IngestOutcome is the result of processing one email. The handler maps it to an
// HTTP status (and thus Mailgun's retry behaviour).
type IngestOutcome struct {
	Status        string // a terminal inbound_email_events.status, or "duplicate"
	DraftsCreated int
	Duplicate     bool // a Message-Id we'd already finished — handler still 200s
}

// =============================================================================
// INGEST — webhook payload → draft expense(s)
// =============================================================================

// Ingest processes one parsed inbound email SYNCHRONOUSLY and returns the terminal
// outcome. It is the claim+process pair run inline, used by the service-level tests
// for deterministic assertions. The webhook handler uses Accept instead, which acks
// before the (slow) processing — see that method.
func (s *EmailInboxService) Ingest(ctx context.Context, in *InboundEmail) (IngestOutcome, error) {
	eventID, done, outcome, err := s.claimOrDuplicate(ctx, in)
	if err != nil || done {
		return outcome, err
	}
	return s.process(ctx, in, eventID)
}

// Accept claims the email SYNCHRONOUSLY (a fast, durable, dedupe-safe write) and
// hands the heavy capture/render work to the background, so the webhook can ack
// Mailgun before processing finishes. This is what fixes the inbound webhook
// timeout: Mailgun's POST no longer waits on GCS uploads + DB writes (+ a possible
// Gotenberg render). It mirrors the OCR Enqueue fire-and-forget pattern.
//
// Tradeoff: acking before processing forfeits Mailgun's automatic retry on a
// transient failure (the background error is logged and the event row is marked
// 'error' for visibility; a manual re-send is dedupe-safe via the content hash).
// A durable queue is the robust follow-up — see BACKLOG.md.
//
// The only errors returned synchronously are the fast ones from claimOrDuplicate:
// a missing Message-Id (validation → 422) or a claim DB failure (internal → 500, so
// Mailgun retries — safe because nothing was processed yet).
func (s *EmailInboxService) Accept(ctx context.Context, in *InboundEmail) (IngestOutcome, error) {
	eventID, done, outcome, err := s.claimOrDuplicate(ctx, in)
	if err != nil || done {
		return outcome, err
	}
	s.runInBackground(func() {
		// The request context ends the moment the handler returns, so use a fresh
		// bounded context for the background work.
		bg, cancel := context.WithTimeout(context.Background(), s.processTimeout)
		defer cancel()
		if _, err := s.process(bg, in, eventID); err != nil {
			// Best-effort: the error is logged and process() already marked the event
			// row 'error'. Mailgun won't retry (we've acked), so this is the record.
			log.Printf("email inbox: background processing of %s failed: %v", in.MessageID, err)
		}
	})
	// "accepted" is an API-only status (never written to the DB); the real terminal
	// status is recorded by process()→finish once the background work completes.
	return IngestOutcome{Status: "accepted"}, nil
}

// claimOrDuplicate runs the fast, synchronous front of ingestion: validate the
// Message-Id and atomically claim it (dedupe). It returns done=true with the
// outcome to return when there is nothing more to do — a missing Message-Id (err
// set) or a genuine duplicate of an already-finished email. Otherwise done=false
// and the caller proceeds to process(eventID).
func (s *EmailInboxService) claimOrDuplicate(ctx context.Context, in *InboundEmail) (eventID uuid.UUID, done bool, outcome IngestOutcome, err error) {
	// Without a Message-Id we can't dedupe. Reject (the handler 422s) rather than
	// risk creating duplicates on every Mailgun retry.
	if strings.TrimSpace(in.MessageID) == "" {
		return uuid.Nil, true, IngestOutcome{}, ErrValidation("inbound email has no Message-Id", nil)
	}

	// --- Claim (dedupe). -----------------------------------------------------
	id, fresh, prevStatus, err := s.claim(ctx, in)
	if err != nil {
		return uuid.Nil, true, IngestOutcome{}, err
	}
	if !fresh && isTerminalStatus(prevStatus) {
		// A genuine duplicate delivery of an email we already finished.
		return uuid.Nil, true, IngestOutcome{Status: "duplicate", Duplicate: true}, nil
	}
	// Otherwise: a fresh email, OR a prior attempt that didn't finish (a 500'd
	// 'received'/'error' row) — reprocess it.
	return id, false, IngestOutcome{}, nil
}

// process runs the heavy part of ingestion: resolve the recipient → sender check →
// capture each attachment → HTML-body fallback → record the terminal outcome. It
// runs synchronously inline (Ingest) or in the background goroutine (Accept). The
// Message-Id is already claimed as eventID.
func (s *EmailInboxService) process(ctx context.Context, in *InboundEmail, eventID uuid.UUID) (IngestOutcome, error) {
	// --- 2. Resolve the recipient address → (claimant user, org). -----------
	localPart, ok := s.resolveLocalPart(in)
	if !ok {
		return s.finish(ctx, eventID, "ignored_unknown_address", uuid.Nil, len(in.Attachments), 0,
			fmt.Sprintf("recipient %q not on inbox domain %q", in.Recipient, s.inboxDomain))
	}
	membership, err := s.authQueries.GetMembershipByInboxLocalPart(ctx, pgtype.Text{String: localPart, Valid: true})
	if errors.Is(err, pgx.ErrNoRows) {
		return s.finish(ctx, eventID, "ignored_unknown_address", uuid.Nil, len(in.Attachments), 0,
			fmt.Sprintf("no active membership for inbox %q", localPart))
	}
	if err != nil {
		return s.fail(ctx, eventID, fmt.Errorf("resolve inbox address: %w", err))
	}
	claimantID, orgID := membership.UserID, membership.OrganisationID

	// --- 3. Sender check: From must be an active member of THIS org. --------
	if !s.senderIsActiveMember(ctx, in.From, orgID) {
		return s.finish(ctx, eventID, "ignored_sender_not_member", orgID, len(in.Attachments), 0,
			fmt.Sprintf("sender %q is not an active member of org %s", in.From, orgID))
	}

	// --- 4. Capture each file attachment as a draft. ------------------------
	var (
		drafts int
		dupes  int
		notes  []string
	)
	for i, att := range in.Attachments {
		// Content-level dedupe: if this exact file already became an expense for
		// this claimant, skip it. Catches the same receipt re-sent as a separate
		// email (a new Message-Id, which the event-level dedupe can't catch).
		if dupID, isDup, err := s.duplicateOf(ctx, claimantID, orgID, att); err != nil {
			return s.fail(ctx, eventID, fmt.Errorf("dedupe attachment %d: %w", i+1, err))
		} else if isDup {
			notes = append(notes, fmt.Sprintf("attachment %d (%s): duplicate of expense %s, skipped", i+1, att.Filename, dupID))
			dupes++
			continue
		}

		if err := s.captureAttachment(ctx, claimantID, orgID, att); err != nil {
			var appErr *AppError
			if errors.As(err, &appErr) && appErr.Code != ErrCodeInternal {
				// A bad file (unsupported type, too large, unreadable): skip it but
				// keep going — other attachments may be fine. Recorded in the note.
				notes = append(notes, fmt.Sprintf("attachment %d (%s): %s", i+1, att.Filename, appErr.Message))
				continue
			}
			// A storage/DB error must NOT be acked: return it so the handler 500s
			// and Mailgun retries (our Message-Id claim makes the retry safe).
			return s.fail(ctx, eventID, fmt.Errorf("capture attachment %d: %w", i+1, err))
		}
		drafts++
	}

	// --- 5. HTML-body fallback (only when no file produced a draft). --------
	// Receipts like Uber/Amazon arrive as an HTML body with no attachment; render
	// it to a PDF and capture that. Attachments win when present, so we never
	// create a duplicate draft for the same email.
	if drafts == 0 && s.renderer != nil {
		created, err := s.captureBody(ctx, claimantID, orgID, in)
		if err != nil {
			return s.fail(ctx, eventID, fmt.Errorf("capture html body: %w", err))
		}
		if created {
			drafts++
		}
	}

	// --- 6. Record the outcome. ---------------------------------------------
	// processed if we created any draft; else if every attachment was a duplicate
	// of an existing one, say so distinctly; else nothing was capturable.
	status := "processed"
	if drafts == 0 {
		if dupes > 0 {
			status = "ignored_duplicate"
		} else {
			status = "ignored_no_attachments"
		}
	}
	return s.finish(ctx, eventID, status, orgID, len(in.Attachments), drafts, strings.Join(notes, "; "))
}

// claim atomically claims the email by Message-Id. It returns the event id and
// whether this was a FRESH claim. On a conflict (we've seen this Message-Id) it
// reads the existing row so the caller can decide: skip a finished email, or
// reprocess one that didn't finish.
func (s *EmailInboxService) claim(ctx context.Context, in *InboundEmail) (id uuid.UUID, fresh bool, prevStatus string, err error) {
	id, claimErr := s.eventQueries.ClaimInboundEmailEvent(ctx, emailinbox.ClaimInboundEmailEventParams{
		ProviderMessageID: in.MessageID,
		Recipient:         in.Recipient,
		Sender:            in.From,
		Subject:           pgNullText(nilIfEmpty(in.Subject)),
	})
	if claimErr == nil {
		return id, true, "received", nil
	}
	if !errors.Is(claimErr, pgx.ErrNoRows) {
		return uuid.Nil, false, "", ErrInternal(fmt.Errorf("claim inbound email: %w", claimErr))
	}
	// Conflict: already seen. Read its current status.
	row, getErr := s.eventQueries.GetInboundEmailEventByMessageID(ctx, in.MessageID)
	if getErr != nil {
		return uuid.Nil, false, "", ErrInternal(fmt.Errorf("lookup inbound email: %w", getErr))
	}
	return row.ID, false, row.Status, nil
}

// finish records the terminal event row and returns the matching outcome.
func (s *EmailInboxService) finish(ctx context.Context, eventID uuid.UUID, status string, orgID uuid.UUID, attachmentCount, drafts int, note string) (IngestOutcome, error) {
	err := s.eventQueries.FinishInboundEmailEvent(ctx, emailinbox.FinishInboundEmailEventParams{
		ID:              eventID,
		Status:          status,
		OrganisationID:  pgUUIDOrNull(orgID),
		AttachmentCount: int32(attachmentCount),
		DraftsCreated:   int32(drafts),
		Note:            pgNullText(nilIfEmpty(note)),
	})
	if err != nil {
		return IngestOutcome{}, ErrInternal(fmt.Errorf("finish inbound email: %w", err))
	}
	return IngestOutcome{Status: status, DraftsCreated: drafts}, nil
}

// fail marks the event 'error' (best-effort, for visibility) and returns the
// underlying cause so the handler 500s. Mailgun then retries; because the row is
// left non-terminal, the retry reprocesses it rather than skipping it.
func (s *EmailInboxService) fail(ctx context.Context, eventID uuid.UUID, cause error) (IngestOutcome, error) {
	_ = s.eventQueries.FinishInboundEmailEvent(ctx, emailinbox.FinishInboundEmailEventParams{
		ID:     eventID,
		Status: "error",
		Note:   pgNullText(nilIfEmpty(truncate(cause.Error(), 500))),
	})
	return IngestOutcome{}, ErrInternal(cause)
}

// duplicateOf hashes the attachment's bytes and asks whether this claimant
// already has a non-deleted expense built from the identical file. A read/open
// error returns "not a duplicate" (capture will surface the bad file properly);
// only a DB lookup error propagates (→ the caller 500s and Mailgun retries).
func (s *EmailInboxService) duplicateOf(ctx context.Context, claimantID, orgID uuid.UUID, att InboundAttachment) (uuid.UUID, bool, error) {
	r, err := att.Open()
	if err != nil {
		return uuid.Nil, false, nil
	}
	if c, ok := r.(io.Closer); ok {
		defer c.Close()
	}
	h := sha256.New()
	if _, err := io.Copy(h, r); err != nil {
		return uuid.Nil, false, nil
	}
	hash := hex.EncodeToString(h.Sum(nil))
	return s.attachments.FindDuplicateReceipt(ctx, orgID, claimantID, hash)
}

// captureAttachment streams one file into the existing capture pipeline. The
// claimant is the inbox owner; CaptureFromReceipt validates the MIME type/size,
// stores the bytes in GCS, creates the skeleton draft, and enqueues OCR.
func (s *EmailInboxService) captureAttachment(ctx context.Context, claimantID, orgID uuid.UUID, att InboundAttachment) error {
	r, err := att.Open()
	if err != nil {
		return ErrValidation(fmt.Sprintf("could not read attachment %q", att.Filename), err)
	}
	if c, ok := r.(io.Closer); ok {
		defer c.Close()
	}
	_, err = s.attachments.CaptureFromReceipt(ctx, claimantID, orgID, emailDocumentType, att.Filename, att.Size, r)
	return err
}

// captureBody renders the email's HTML body (or plain-text wrapped in HTML) to a
// PDF and captures it. Returns false (no error) when there's no body to render.
func (s *EmailInboxService) captureBody(ctx context.Context, claimantID, orgID uuid.UUID, in *InboundEmail) (bool, error) {
	htmlDoc := in.BodyHTML
	if strings.TrimSpace(htmlDoc) == "" {
		if strings.TrimSpace(in.BodyPlain) == "" {
			return false, nil // nothing to render
		}
		// Wrap the plain-text body so Gotenberg has a document to render. Escape
		// it so stray angle brackets in the text aren't interpreted as markup.
		htmlDoc = "<!doctype html><html><body><pre>" + html.EscapeString(in.BodyPlain) + "</pre></body></html>"
	}
	pdf, err := s.renderer.RenderPDF(ctx, htmlDoc)
	if err != nil {
		return false, err // transient → caller 500s and Mailgun retries
	}
	if _, err := s.attachments.CaptureFromReceipt(ctx, claimantID, orgID, emailDocumentType, "email-body.pdf", int64(len(pdf)), bytes.NewReader(pdf)); err != nil {
		return false, err
	}
	return true, nil
}

// senderIsActiveMember reports whether the From address belongs to an active
// member of orgID. This is the authorisation gate for the (human-readable, not
// secret) inbox address. An unknown sender or any lookup miss → not a member.
func (s *EmailInboxService) senderIsActiveMember(ctx context.Context, from string, orgID uuid.UUID) bool {
	email := normalizeEmail(from)
	if email == "" {
		return false
	}
	user, err := s.authQueries.GetUserByEmail(ctx, email)
	if err != nil {
		return false
	}
	m, err := s.authQueries.GetMembership(ctx, auth.GetMembershipParams{OrganisationID: orgID, UserID: user.ID})
	if err != nil {
		return false
	}
	return m.Status == "active"
}

// resolveLocalPart finds the local part of the address on our inbox domain. It
// prefers the envelope Recipient (reliable for catch-all, incl. Bcc) and falls
// back to scanning the To header (which may list several recipients).
func (s *EmailInboxService) resolveLocalPart(in *InboundEmail) (string, bool) {
	if lp, ok := s.localPartForDomain(in.Recipient); ok {
		return lp, true
	}
	if list, err := mail.ParseAddressList(in.ToHeader); err == nil {
		for _, a := range list {
			if lp, ok := s.localPartForDomain(a.Address); ok {
				return lp, true
			}
		}
	}
	return "", false
}

// localPartForDomain extracts the lower-cased local part of an address that is on
// our inbox domain. Accepts a bare address or a "Name <addr>" form. Returns
// ("", false) when the address isn't on s.inboxDomain.
func (s *EmailInboxService) localPartForDomain(addr string) (string, bool) {
	addr = strings.TrimSpace(addr)
	if addr == "" || s.inboxDomain == "" {
		return "", false
	}
	if a, err := mail.ParseAddress(addr); err == nil {
		addr = a.Address
	}
	at := strings.LastIndex(addr, "@")
	if at < 0 {
		return "", false
	}
	local := strings.ToLower(strings.TrimSpace(addr[:at]))
	domain := strings.ToLower(strings.TrimSpace(addr[at+1:]))
	if local == "" || domain != s.inboxDomain {
		return "", false
	}
	return local, true
}

// =============================================================================
// INBOX ADDRESS — read-only, auto-created per (user, org)
// =============================================================================

// GetOrCreateInboxAddress returns the caller's receipt-inbox address for this
// organisation, generating (provisioning) the local part lazily on first call.
// Returns "" when the channel is disabled (no INBOX_DOMAIN).
func (s *EmailInboxService) GetOrCreateInboxAddress(ctx context.Context, userID, orgID uuid.UUID) (string, error) {
	if s.inboxDomain == "" {
		return "", nil
	}
	m, err := s.authQueries.GetMembership(ctx, auth.GetMembershipParams{OrganisationID: orgID, UserID: userID})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", ErrForbidden("you are not a member of this organisation")
		}
		return "", ErrInternal(fmt.Errorf("load membership: %w", err))
	}
	if m.InboxLocalPart.Valid && m.InboxLocalPart.String != "" {
		return m.InboxLocalPart.String + "@" + s.inboxDomain, nil
	}

	base, err := s.baseLocalPart(ctx, userID, orgID)
	if err != nil {
		return "", err
	}
	localPart, err := s.provisionLocalPart(ctx, userID, orgID, base)
	if err != nil {
		return "", err
	}
	return localPart + "@" + s.inboxDomain, nil
}

// baseLocalPart builds the human-readable base "{name}.{org}" for an address,
// e.g. "aydin.gunal.acme-ltd". Falls back gracefully when names/slug are empty.
func (s *EmailInboxService) baseLocalPart(ctx context.Context, userID, orgID uuid.UUID) (string, error) {
	user, err := s.authQueries.GetUser(ctx, userID)
	if err != nil {
		return "", ErrInternal(fmt.Errorf("load user: %w", err))
	}
	org, err := s.authQueries.GetOrganisation(ctx, orgID)
	if err != nil {
		return "", ErrInternal(fmt.Errorf("load organisation: %w", err))
	}

	name := slugify(user.FirstName + " " + user.LastName)
	orgSlug := ""
	if org.Slug.Valid {
		orgSlug = slugify(org.Slug.String) // already URL-safe; slugify just normalises
	}
	if orgSlug == "" {
		orgSlug = slugify(org.Name)
	}

	parts := make([]string, 0, 2)
	if name != "" {
		parts = append(parts, name)
	}
	if orgSlug != "" {
		parts = append(parts, orgSlug)
	}
	base := strings.Join(parts, ".")
	if base == "" {
		base = "receipts" // last resort; the uniqueness loop disambiguates it
	}
	return base, nil
}

// provisionLocalPart claims a unique local part for the membership, trying base,
// base.2, base.3, … until one is free. It is idempotent under races: if another
// request set the address first, we re-read and return that.
func (s *EmailInboxService) provisionLocalPart(ctx context.Context, userID, orgID uuid.UUID, base string) (string, error) {
	for attempt := 1; attempt <= maxInboxLocalPartAttempts; attempt++ {
		candidate := base
		if attempt > 1 {
			candidate = fmt.Sprintf("%s.%d", base, attempt)
		}
		got, err := s.authQueries.SetMembershipInboxLocalPart(ctx, auth.SetMembershipInboxLocalPartParams{
			OrganisationID: orgID,
			UserID:         userID,
			InboxLocalPart: pgtype.Text{String: candidate, Valid: true},
		})
		if err == nil {
			return got.String, nil
		}
		if errors.Is(err, pgx.ErrNoRows) {
			// Our membership already has a local part (set concurrently) — re-read.
			m, gerr := s.authQueries.GetMembership(ctx, auth.GetMembershipParams{OrganisationID: orgID, UserID: userID})
			if gerr != nil {
				return "", ErrInternal(fmt.Errorf("reload membership: %w", gerr))
			}
			if m.InboxLocalPart.Valid {
				return m.InboxLocalPart.String, nil
			}
			return "", ErrInternal(errors.New("inbox address vanished after concurrent set"))
		}
		if isUniqueViolation(err) {
			continue // candidate taken by another membership; try the next suffix
		}
		return "", ErrInternal(fmt.Errorf("set inbox address: %w", err))
	}
	return "", ErrInternal(errors.New("could not generate a unique inbox address"))
}

// =============================================================================
// SMALL HELPERS
// =============================================================================

// isTerminalStatus reports whether an event has reached a final state and so a
// re-delivery of its Message-Id should be skipped (vs reprocessed).
func isTerminalStatus(status string) bool {
	switch status {
	case "processed", "ignored_unknown_address", "ignored_sender_not_member", "ignored_no_attachments", "ignored_duplicate":
		return true
	default:
		return false
	}
}

// isUniqueViolation reports whether err is a PostgreSQL unique-constraint
// violation (SQLSTATE 23505).
func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}

// normalizeEmail extracts and lower-cases the addr-spec from a From value, which
// may be a bare address or a "Name <addr>" header. Returns "" if unparseable/empty.
func normalizeEmail(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	if addr, err := mail.ParseAddress(raw); err == nil {
		return strings.ToLower(addr.Address)
	}
	return strings.ToLower(raw)
}

// slugify lower-cases, strips accents (ü→u), and turns any run of other
// characters into a single '.', keeping [a-z0-9-]. e.g. "Aydin Günal" → "aydin.gunal".
func slugify(s string) string {
	s = strings.ToLower(foldASCII(s))
	var b strings.Builder
	prevSep := true // treat the start as a separator so we never lead with '.'
	for _, r := range s {
		switch {
		case (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-':
			b.WriteRune(r)
			prevSep = false
		case !prevSep:
			b.WriteByte('.')
			prevSep = true
		}
	}
	return strings.Trim(b.String(), ".-")
}

// foldASCII removes diacritics by decomposing (NFD), dropping combining marks,
// then recomposing (NFC). "Günal" → "Gunal". Falls back to the input on error.
func foldASCII(s string) string {
	t := transform.Chain(norm.NFD, runes.Remove(runes.In(unicode.Mn)), norm.NFC)
	out, _, err := transform.String(t, s)
	if err != nil {
		return s
	}
	return out
}

// nilIfEmpty returns nil for "" (so pgNullText writes SQL NULL) else a pointer.
func nilIfEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// pgUUIDOrNull wraps a uuid in pgtype.UUID, mapping uuid.Nil to SQL NULL.
func pgUUIDOrNull(id uuid.UUID) pgtype.UUID {
	if id == uuid.Nil {
		return pgtype.UUID{Valid: false}
	}
	return pgtype.UUID{Bytes: id, Valid: true}
}

// truncate caps a string at n bytes (used to keep error notes bounded).
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
