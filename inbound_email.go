package main

// inbound_email.go
// =============================================================================
// Types + signature verification for the email-to-expense channel.
//
// InboundEmail is the provider-NEUTRAL shape EmailInboxService works with. The
// Mailgun-specific webhook handler (email_inbox_handler.go) parses the POST into
// this struct, so the service carries no Mailgun knowledge and tests can build an
// InboundEmail directly. Same idea as the Storage / DocumentExtractor /
// EmailSender interfaces — keep the vendor at the edge.
// =============================================================================

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"io"
)

// InboundEmail is one received email, already parsed out of the webhook payload.
type InboundEmail struct {
	MessageID   string // the 'Message-Id' header — our idempotency / dedupe key
	Recipient   string // the address it was delivered to (Mailgun's envelope recipient)
	ToHeader    string // the raw To header — a fallback when Recipient is empty
	From        string // the From-header email (the submitter)
	Subject     string
	BodyHTML    string // present for HTML-body receipts (no file attached)
	BodyPlain   string // fallback body when there's no HTML part
	Attachments []InboundAttachment
}

// InboundAttachment is one file carried by the email. Open returns a fresh reader
// over the bytes (which we've buffered from the webhook request), so the service
// can stream it straight into the existing capture pipeline.
type InboundAttachment struct {
	Filename string
	Size     int64
	Open     func() (io.ReadSeeker, error)
}

// verifyMailgunSignature reports whether an inbound webhook was really signed by
// Mailgun. Mailgun signs each POST with our HTTP signing key:
//
//	signature == hex( HMAC-SHA256(key = signingKey, message = timestamp + token) )
//
// We recompute it and compare in constant time. An attacker who doesn't know the
// signing key can't forge a valid (timestamp, token, signature) triple, so this
// is what authenticates the public webhook — it carries no bearer token.
func verifyMailgunSignature(signingKey, timestamp, token, signature string) bool {
	if signingKey == "" || signature == "" {
		return false
	}
	mac := hmac.New(sha256.New, []byte(signingKey))
	mac.Write([]byte(timestamp + token))
	expected := hex.EncodeToString(mac.Sum(nil))
	// hmac.Equal compares in constant time — it won't leak, via timing, how many
	// leading bytes of the signature matched.
	return hmac.Equal([]byte(expected), []byte(signature))
}
