package main

// email.go
// =============================================================================
// EmailSender — the abstraction over outbound email.
//
// Why an interface (mirroring storage.go)?
//   - The handlers should depend on *what* we do (send a message) — not on a
//     concrete SMTP client or provider SDK. Swapping SMTP for SES/SendGrid later
//     is "write one new implementation", not a rewrite.
//   - It keeps transport (how a message is delivered) separate from content
//     (what the message says). Content/templating lives in email_content.go.
//
// Implementations:
//   - smtpSender (email_smtp.go) — real delivery via the standard library.
//   - logSender  (below)         — logs the message instead of sending; used in
//                                  dev/tests when no SMTP server is configured,
//                                  so the password-reset link shows up in logs.
// =============================================================================

import (
	"context"
	"log"
)

// EmailSender delivers a plain-text email to a single recipient. The caller
// builds the subject + body (see email_content.go); the sender is pure transport.
type EmailSender interface {
	Send(ctx context.Context, to, subject, body string) error
}

// logSender is an EmailSender that doesn't send — it logs the message. It lets
// the whole flow work without a mail server (the reset link is in the logs).
type logSender struct{}

func newLogSender() *logSender { return &logSender{} }

func (s *logSender) Send(_ context.Context, to, subject, body string) error {
	log.Printf("[email:log] to=%s subject=%q\n%s\n", to, subject, body)
	return nil
}
