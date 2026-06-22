package email

// email.go
// =============================================================================
// Sender — the abstraction over outbound email.
//
// Why an interface (mirroring internal/storage)?
//   - The callers should depend on *what* we do (send a message) — not on a
//     concrete SMTP client or provider SDK. Swapping SMTP for SES/SendGrid later
//     is "write one new implementation", not a rewrite.
//   - It keeps transport (how a message is delivered) separate from content
//     (what the message says). Content/templating lives with each caller (e.g.
//     the password-reset email in internal/userauth/email.go).
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

// Sender delivers a plain-text email to a single recipient. The caller
// builds the subject + body; the sender is pure transport.
type Sender interface {
	Send(ctx context.Context, to, subject, body string) error
}

// logSender is an Sender that doesn't send — it logs the message. It lets
// the whole flow work without a mail server (the reset link is in the logs).
type logSender struct{}

func NewLogSender() *logSender { return &logSender{} }

func (s *logSender) Send(_ context.Context, to, subject, body string) error {
	log.Printf("[email:log] to=%s subject=%q\n%s\n", to, subject, body)
	return nil
}
