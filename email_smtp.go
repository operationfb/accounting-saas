package main

// email_smtp.go
// =============================================================================
// smtpSender — the real EmailSender, using the standard library net/smtp.
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
	"net/smtp"
	"strings"
)

// smtpConfig holds the SMTP connection + sender details (read from env in main).
type smtpConfig struct {
	Host     string // e.g. "smtp.eu.mailgun.org" or "localhost"
	Port     string // e.g. "587" (STARTTLS) or "1025" (Mailpit)
	Username string // empty → connect without auth (e.g. local Mailpit)
	Password string
	From     string // From header + envelope sender, e.g. "no-reply@example.com"
}

type smtpSender struct {
	cfg smtpConfig
}

func newSMTPSender(cfg smtpConfig) *smtpSender { return &smtpSender{cfg: cfg} }

// Send builds a minimal RFC-5322 plain-text message and delivers it. PlainAuth
// is used only when a username is configured (net/smtp refuses PLAIN over an
// unencrypted connection except to localhost, so creds aren't sent in clear).
func (s *smtpSender) Send(_ context.Context, to, subject, body string) error {
	addr := net.JoinHostPort(s.cfg.Host, s.cfg.Port)

	var auth smtp.Auth
	if s.cfg.Username != "" {
		auth = smtp.PlainAuth("", s.cfg.Username, s.cfg.Password, s.cfg.Host)
	}

	if err := smtp.SendMail(addr, auth, s.cfg.From, []string{to}, buildMessage(s.cfg.From, to, subject, body)); err != nil {
		return fmt.Errorf("smtp send to %s: %w", to, err)
	}
	return nil
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
