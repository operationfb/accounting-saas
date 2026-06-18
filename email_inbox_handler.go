package main

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
	"io"
	"mime/multipart"
	"net/http"
	"sort"
	"strings"

	"github.com/gin-gonic/gin"
)

// maxInboundEmailBytes hard-caps the whole inbound webhook body. Receipts are
// small; this leaves room for the body text plus a couple of attachments. An
// oversized email fails to parse here (a store-and-fetch fallback for big emails
// is a backlog item).
const maxInboundEmailBytes int64 = 35 << 20 // 35 MiB

// handleMailgunInbound handles POST /api/v1/webhooks/mailgun/inbound.
func (s *Server) handleMailgunInbound(c *gin.Context) {
	// Cap the body before parsing so an oversized/abusive POST can't exhaust us.
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxInboundEmailBytes)

	// Mailgun forwards multipart/form-data (parsed fields + inline attachment
	// files). Parsing buffers up to MaxMultipartMemory in RAM, the rest to temp.
	form, err := c.MultipartForm()
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "could not parse multipart form (or it was too large)"})
		return
	}

	val := func(key string) string {
		if vs := form.Value[key]; len(vs) > 0 {
			return vs[0]
		}
		return ""
	}

	// --- Authenticate FIRST: verify Mailgun's HMAC signature. ----------------
	// An attacker without the signing key can't forge (timestamp, token, signature).
	if !verifyMailgunSignature(s.mailgunSigningKey, val("timestamp"), val("token"), val("signature")) {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid signature"})
		return
	}

	// --- Build the provider-neutral InboundEmail. ----------------------------
	in := &InboundEmail{
		MessageID:   val("Message-Id"),
		Recipient:   val("recipient"),
		ToHeader:    val("To"),
		From:        val("from"),
		Subject:     val("subject"),
		BodyHTML:    val("body-html"),
		BodyPlain:   val("body-plain"),
		Attachments: inboundAttachmentsFromForm(form.File),
	}

	// --- Hand off to the (provider-agnostic) service. ------------------------
	outcome, err := s.emailInboxService.Ingest(c.Request.Context(), in)
	if err != nil {
		appErr := AsAppError(err)
		if appErr.Code == ErrCodeInternal {
			logInternalError(c, appErr.Err)
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
// parts into provider-neutral InboundAttachments. Each Open() reopens the part,
// giving the capture pipeline a fresh io.ReadSeeker over the (buffered) bytes.
func inboundAttachmentsFromForm(files map[string][]*multipart.FileHeader) []InboundAttachment {
	// Sort the field names for deterministic ordering (handy for tests/notes).
	names := make([]string, 0, len(files))
	for name := range files {
		if strings.HasPrefix(name, "attachment-") {
			names = append(names, name)
		}
	}
	sort.Strings(names)

	var out []InboundAttachment
	for _, name := range names {
		for _, fh := range files[name] {
			fh := fh // capture per-iteration for the closure
			out = append(out, InboundAttachment{
				Filename: fh.Filename,
				Size:     fh.Size,
				Open: func() (io.ReadSeeker, error) {
					// *multipart.FileHeader.Open() returns a multipart.File, which
					// is an io.ReadSeeker (and io.Closer — closed by the service).
					return fh.Open()
				},
			})
		}
	}
	return out
}

// handleGetInboxAddress handles GET /api/v1/inbox-address. It returns the
// caller's receipt-inbox address for their organisation. When the channel is
// disabled (no INBOX_DOMAIN), it reports enabled:false rather than erroring, so
// the SPA can simply hide the feature.
func (s *Server) handleGetInboxAddress(c *gin.Context) {
	if s.emailInboxService == nil {
		c.JSON(http.StatusOK, gin.H{"enabled": false, "address": ""})
		return
	}
	userID := getAuthUserID(c)
	orgID := getAuthOrgID(c)
	address, err := s.emailInboxService.GetOrCreateInboxAddress(c.Request.Context(), userID, orgID)
	if err != nil {
		respondError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"enabled": address != "", "address": address})
}
