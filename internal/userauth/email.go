package main

// email_content.go
// =============================================================================
// Email content / templating — kept SEPARATE from transport (EmailSender).
//
// Each email type is a subject + a text/template body. A handler gathers the
// data, calls the builder here to render (subject, body), then hands those to
// emailSender.Send(). Plain text for now; an HTML/multipart version can be
// added later without touching the handlers or the transport.
// =============================================================================

import (
	"bytes"
	"text/template"
)

// render executes tmpl with data and returns the produced string.
func render(tmpl *template.Template, data any) (string, error) {
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return "", err
	}
	return buf.String(), nil
}

// -----------------------------------------------------------------------------
// Password reset
// -----------------------------------------------------------------------------

const passwordResetSubject = "Reset your password"

// passwordResetData is the data the password-reset template renders.
type passwordResetData struct {
	FirstName     string
	ResetLink     string
	ExpiryMinutes int
}

// passwordResetTmpl is the plain-text body of the password-reset email.
var passwordResetTmpl = template.Must(template.New("password_reset").Parse(
	`Hi {{.FirstName}},

We received a request to reset the password for your account. Use the link below
to choose a new password. It expires in {{.ExpiryMinutes}} minutes and can be
used only once:

{{.ResetLink}}

If you didn't request this, you can safely ignore this email — your password
won't change.
`))

// buildPasswordResetEmail renders the password-reset subject + body.
func buildPasswordResetEmail(firstName, resetLink string, expiryMinutes int) (subject, body string, err error) {
	body, err = render(passwordResetTmpl, passwordResetData{
		FirstName:     firstName,
		ResetLink:     resetLink,
		ExpiryMinutes: expiryMinutes,
	})
	if err != nil {
		return "", "", err
	}
	return passwordResetSubject, body, nil
}
