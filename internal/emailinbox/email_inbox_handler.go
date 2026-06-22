package emailinbox

// email_inbox_handler.go
// =============================================================================
// HTTP handlers for the email-to-expense channel.
//
//   POST /api/v1/webhooks/mailgun/inbound  (PUBLIC) — Mailgun forwards a parsed
//        inbound email here. Authenticated by Mailgun's HMAC signature, not a
//        bearer token. This is the Mailgun-SPECIFIC edge: it parses the multipart
//        payload into a provider-neutral InboundEmail and hands it to the service.
//
//   GET  /api/v1/inbox-address             (AUTHED) — returns the caller's
//        receipt-inbox address for their organisation (generated lazily).
//
// Webhook response semantics (Mailgun retries non-2xx for hours):
//   401 — bad/missing signature (reject).
//   200 — processed / deliberately ignored / duplicate (stop retrying).
//   422 — deterministically bad payload, e.g. no Message-Id (retrying won't help).
//   500 — transient storage/DB failure (let Mailgun retry; the Message-Id claim
//         makes the retry safe — "persist then ack").
// =============================================================================

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"mime/multipart"
	"net/http"
	"sort"
	"strings"

	"github.com/gin-gonic/gin"

	kernel "github.com/operationfb/accounting-saas/internal/kernel"
	"github.com/operationfb/accounting-saas/token"
)

// Handler is the HTTP boundary for the email-to-expense channel.
type Handler struct {
	svc        *Service // may be nil when the channel is unconfigured (INBOX_DOMAIN unset)
	signingKey string   // Mailgun HMAC key; empty disables the inbound webhook
}

// NewHandler builds the handler. svc may be nil (then /inbox-address reports
// enabled:false and the webhook is not mounted).
func NewHandler(svc *Service, signingKey string) *Handler {
	return &Handler{svc: svc, signingKey: signingKey}
}

// RegisterRoutes mounts the channel's routes. /inbox-address is ALWAYS mounted
// (authed; the handler reports enabled:false when the channel is off). The inbound
// webhook is PUBLIC — it carries no bearer token and is authenticated by Mailgun's
// HMAC signature in the handler — so it's mounted only when fully configured.
func (h *Handler) RegisterRoutes(r *gin.Engine, tokenMaker token.Maker) {
	authed := r.Group("/api/v1")
	authed.Use(kernel.AuthMiddleware(tokenMaker))
	authed.GET("/inbox-address", h.handleGetInboxAddress)

	if h.svc != nil && h.signingKey != "" {
		r.POST("/api/v1/webhooks/mailgun/inbound", h.handleMailgunInbound)
	}
}

// maxInboundEmailBytes hard-caps the whole inbound webhook body. Receipts are
// small; this leaves room for the body text plus a couple of attachments. An
// oversized email fails to parse here (a store-and-fetch fallback for big emails
// is a backlog item).
const maxInboundEmailBytes int64 = 35 << 20 // 35 MiB

// handleMailgunInbound handles POST /api/v1/webhooks/mailgun/inbound.
func (h *Handler) handleMailgunInbound(c *gin.Context) {
	// Cap the body before parsing so an oversized/abusive POST can't exhaust us.
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxInboundEmailBytes)

	// Mailgun uses TWO content types: multipart/form-data when the email HAS
	// attachments, and application/x-www-form-urlencoded when it does NOT. c.PostForm
	// reads field values from either, so we must not reject attachment-less emails
	// (e.g. HTML-body receipts) — they flow on to the HTML-body render fallback.
	val := func(key string) string { return c.PostForm(key) }

	// Attachment files only exist on a multipart body; tolerate a non-multipart
	// (urlencoded) body, which simply has no files.
	var files map[string][]*multipart.FileHeader
	if mf, mferr := c.MultipartForm(); mferr == nil && mf != nil {
		files = mf.File
	}

	// --- Authenticate FIRST: verify Mailgun's HMAC signature. ----------------
	// An attacker without the signing key can't forge (timestamp, token, signature).
	if !verifyMailgunSignature(h.signingKey, val("timestamp"), val("token"), val("signature")) {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid signature"})
		return
	}

	// --- Build the provider-neutral InboundEmail. ----------------------------
	// Mailgun delivers inline body images (logos/signatures referenced via cid:)
	// as attachment-N parts too, and lists them in content-id-map. Skip those so we
	// capture only genuine attachments (e.g. the receipt PDF), not the logo.
	inline := inlineAttachmentNames(val("content-id-map"))
	logInboundAttachments(val("Message-Id"), files, inline)

	in := &InboundEmail{
		MessageID:   val("Message-Id"),
		Recipient:   val("recipient"),
		ToHeader:    val("To"),
		From:        val("from"),
		Subject:     val("subject"),
		BodyHTML:    val("body-html"),
		BodyPlain:   val("body-plain"),
		Attachments: inboundAttachmentsFromForm(files, inline),
	}

	// --- Hand off to the (provider-agnostic) service. ------------------------
	// Accept claims the email synchronously then processes it in the BACKGROUND, so
	// we ack Mailgun fast (the capture/GCS/render work used to run on this request
	// path and time Mailgun out). The request context is fine here: Accept only uses
	// it for the fast synchronous claim, and the background work gets its own.
	outcome, err := h.svc.Accept(c.Request.Context(), in)
	if err != nil {
		appErr := kernel.AsAppError(err)
		if appErr.Code == kernel.ErrCodeInternal {
			kernel.LogInternalError(c, appErr.Err)
			// Transient: we did NOT durably handle it → 500 so Mailgun retries.
			c.JSON(http.StatusInternalServerError, gin.H{"error": "temporary failure; please retry"})
			return
		}
		// Deterministically bad payload (e.g. missing Message-Id) → don't retry.
		c.JSON(http.StatusUnprocessableEntity, gin.H{"error": appErr.ClientResponse()})
		return
	}

	// Processed, deliberately ignored, or a duplicate: 200 so Mailgun stops.
	c.JSON(http.StatusOK, gin.H{
		"status":         outcome.Status,
		"drafts_created": outcome.DraftsCreated,
	})
}

// inboundAttachmentsFromForm turns Mailgun's "attachment-1".."attachment-N" file
// parts into provider-neutral InboundAttachments. It reads each part fully into
// memory NOW (during the request) so the bytes survive the background processing
// the webhook hands off to — the multipart temp files are tied to the request and
// may be removed once the handler returns. Each Open() then returns a fresh reader
// over those buffered bytes. The 35 MiB body cap bounds the total memory held.
func inboundAttachmentsFromForm(files map[string][]*multipart.FileHeader, inline map[string]bool) []InboundAttachment {
	// Sort the field names for deterministic ordering (handy for tests/notes).
	names := make([]string, 0, len(files))
	for name, fhs := range files {
		if !strings.HasPrefix(name, "attachment-") {
			continue
		}
		// Skip inline body images (logos/signatures embedded via cid: in the HTML
		// body) — but ONLY when they are image/* types. Many email clients (Outlook,
		// iOS Mail) assign a Content-ID to every MIME part including real PDF
		// attachments; filtering on content-id-map alone would discard those too.
		if inline[name] && isInlineImageGroup(fhs) {
			continue
		}
		names = append(names, name)
	}
	sort.Strings(names)

	var out []InboundAttachment
	for _, name := range names {
		for _, fh := range files[name] {
			buf, err := readMultipartFile(fh)
			if err != nil {
				// A part we can't read is skipped (not fatal) — other attachments may
				// be fine, and a wholly-empty email falls through to the body render.
				log.Printf("mailgun inbound: could not read part %s (%s): %v", name, fh.Filename, err)
				continue
			}
			out = append(out, InboundAttachment{
				Filename: fh.Filename,
				Size:     int64(len(buf)),
				Open: func() (io.ReadSeeker, error) {
					// Fresh independent reader over the buffered bytes on each call —
					// the service opens twice (hash, then upload).
					return bytes.NewReader(buf), nil
				},
			})
		}
	}
	return out
}

// readMultipartFile reads one multipart part fully into memory.
func readMultipartFile(fh *multipart.FileHeader) ([]byte, error) {
	f, err := fh.Open()
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return io.ReadAll(f)
}

// inlineAttachmentNames parses Mailgun's content-id-map — a JSON object mapping a
// Content-ID header to the attachment field name it correlates to, e.g.
// {"<logo@host>": "attachment-2"} — and returns the set of attachment field names
// that are inline body images (logos/signatures referenced via cid:). Those must
// NOT be captured as receipts. We collect any "attachment-N" token from both keys
// and values so we're robust to the map's direction. An empty/absent/unparseable
// map yields an empty set (capture everything, the prior behaviour).
func inlineAttachmentNames(contentIDMap string) map[string]bool {
	inline := map[string]bool{}
	if strings.TrimSpace(contentIDMap) == "" {
		return inline
	}
	var m map[string]string
	if err := json.Unmarshal([]byte(contentIDMap), &m); err != nil {
		log.Printf("mailgun inbound: could not parse content-id-map (%v) — capturing all attachments", err)
		return inline
	}
	for k, v := range m {
		if isAttachmentField(k) {
			inline[k] = true
		}
		if isAttachmentField(v) {
			inline[v] = true
		}
	}
	return inline
}

// isAttachmentField reports whether s is a Mailgun attachment field name —
// "attachment-" followed by one or more digits (e.g. "attachment-2").
func isAttachmentField(s string) bool {
	const prefix = "attachment-"
	if !strings.HasPrefix(s, prefix) {
		return false
	}
	digits := s[len(prefix):]
	if digits == "" {
		return false
	}
	for _, r := range digits {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// logInboundAttachments records which attachment parts arrived and which were
// skipped as inline body images, so an unexpected capture is debuggable from logs.
func logInboundAttachments(messageID string, files map[string][]*multipart.FileHeader, inline map[string]bool) {
	var captured, skipped []string
	for name, fhs := range files {
		if !strings.HasPrefix(name, "attachment-") {
			continue
		}
		for _, fh := range fhs {
			label := fmt.Sprintf("%s:%s(%s)", name, fh.Filename, fh.Header.Get("Content-Type"))
			if inline[name] && isInlineImageGroup(fhs) {
				skipped = append(skipped, label)
			} else {
				captured = append(captured, label)
			}
		}
	}
	sort.Strings(captured)
	sort.Strings(skipped)
	log.Printf("mailgun inbound %s: capturing=%v inline-skipped=%v", messageID, captured, skipped)
}

// isInlineImageGroup reports whether every FileHeader in the slice has an
// image/* Content-Type. Inline body images (logos/signatures referenced via cid:)
// are always image types; real document attachments (PDFs, Office files) are not.
// This guards against discarding a PDF just because the sender's email client gave
// it a Content-ID (which would put it in Mailgun's content-id-map).
func isInlineImageGroup(fhs []*multipart.FileHeader) bool {
	if len(fhs) == 0 {
		return false
	}
	for _, fh := range fhs {
		ct := strings.ToLower(strings.TrimSpace(fh.Header.Get("Content-Type")))
		// Strip parameters: "image/png; name=logo.png" → "image/png"
		if i := strings.Index(ct, ";"); i >= 0 {
			ct = strings.TrimSpace(ct[:i])
		}
		if !strings.HasPrefix(ct, "image/") {
			return false
		}
	}
	return true
}

// handleGetInboxAddress handles GET /api/v1/inbox-address. It returns the
// caller's receipt-inbox address for their organisation. When the channel is
// disabled (no INBOX_DOMAIN), it reports enabled:false rather than erroring, so
// the SPA can simply hide the feature.
func (h *Handler) handleGetInboxAddress(c *gin.Context) {
	if h.svc == nil {
		c.JSON(http.StatusOK, gin.H{"enabled": false, "address": ""})
		return
	}
	userID := kernel.GetAuthUserID(c)
	orgID := kernel.GetAuthOrgID(c)
	address, err := h.svc.GetOrCreateInboxAddress(c.Request.Context(), userID, orgID)
	if err != nil {
		kernel.RespondError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"enabled": address != "", "address": address})
}
