package email

// email_smtp.go
// =============================================================================
// smtpSender — the real Sender, using the standard library net/smtp.
//
// No third-party dependency. Works with:
//   - a local catch-all mailbox (Mailpit/MailHog) — no auth, plain connection;
//   - any SMTP provider (AWS SES / SendGrid / Mailgun) — net/smtp upgrades to
//     STARTTLS when the server advertises it, and authenticates with PLAIN when
//     a username/password is supplied.
// =============================================================================

import (
	"context"
	"fmt"
	"net"
	"net/mail"
	"net/smtp"
	"strings"
)

// Config holds the SMTP connection + sender details (read from env in main).
type Config struct {
	Host     string // e.g. "smtp.eu.mailgun.org" or "localhost"
	Port     string // e.g. "587" (STARTTLS) or "1025" (Mailpit)
	Username string // empty → connect without auth (e.g. local Mailpit)
	Password string
	From     string // envelope sender + From-header address, e.g. "no-reply@example.com"
	FromName string // optional display name shown in the From header, e.g. "Kontala"
}

type smtpSender struct {
	cfg Config
}

func NewSMTPSender(cfg Config) *smtpSender { return &smtpSender{cfg: cfg} }

// Send builds a minimal RFC-5322 plain-text message and delivers it. PlainAuth
// is used only when a username is configured (net/smtp refuses PLAIN over an
// unencrypted connection except to localhost, so creds aren't sent in clear).
func (s *smtpSender) Send(_ context.Context, to, subject, body string) error {
	addr := net.JoinHostPort(s.cfg.Host, s.cfg.Port)

	var auth smtp.Auth
	if s.cfg.Username != "" {
		auth = smtp.PlainAuth("", s.cfg.Username, s.cfg.Password, s.cfg.Host)
	}

	// Envelope sender stays the bare address (s.cfg.From); only the From HEADER
	// carries the optional display name, e.g. "Kontala <hello@kontala.com>".
	if err := smtp.SendMail(addr, auth, s.cfg.From, []string{to}, buildMessage(fromHeader(s.cfg.From, s.cfg.FromName), to, subject, body)); err != nil {
		return fmt.Errorf("smtp send to %s: %w", to, err)
	}
	return nil
}

// fromHeader builds the From-header value. With a display name it returns the
// RFC-5322 "Name <addr>" form (mail.Address handles any needed quoting/encoding);
// without one it returns the bare address unchanged.
func fromHeader(from, name string) string {
	if strings.TrimSpace(name) == "" {
		return from
	}
	addr := mail.Address{Name: name, Address: from}
	return addr.String()
}

// buildMessage assembles a minimal plain-text email (headers + body) with the
// CRLF line endings the SMTP wire format requires.
func buildMessage(from, to, subject, body string) []byte {
	// Normalise the body to CRLF regardless of how the template produced it.
	b := strings.ReplaceAll(strings.ReplaceAll(body, "\r\n", "\n"), "\n", "\r\n")

	var sb strings.Builder
	fmt.Fprintf(&sb, "From: %s\r\n", from)
	fmt.Fprintf(&sb, "To: %s\r\n", to)
	fmt.Fprintf(&sb, "Subject: %s\r\n", subject)
	sb.WriteString("MIME-Version: 1.0\r\n")
	sb.WriteString("Content-Type: text/plain; charset=\"utf-8\"\r\n")
	sb.WriteString("\r\n")
	sb.WriteString(b)
	return []byte(sb.String())
}
