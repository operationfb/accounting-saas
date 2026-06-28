package kernel

// payroll.go (package kernel)
// =============================================================================
// Shared parse-and-validate helpers for the three optional payroll-identity
// fields on a user (National Insurance number, personal UTR, date of birth).
//
// They live in the kernel because BOTH the self path (internal/userauth, the My
// Details / User Details form) and the admin path (internal/members, editing
// another user) write these columns — keeping the rules in one place stops the
// two paths from drifting apart.
//
// Each helper takes the optional inbound *string and returns the pgtype value
// ready to store: a nil or blank input becomes a NULL column (clearing it), a
// non-blank input is normalised and format-checked, and a bad value returns a
// 422 (ErrValidation). Format is enforced HERE, not by a DB CHECK, so an import
// or a partially-filled form is never rejected at the column (see the schema).
// =============================================================================

import (
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
)

// UK National Insurance number: two prefix letters, six digits, one suffix
// letter (e.g. SY598539D). This is the shape check only — we deliberately don't
// enforce the full HMRC prefix-exclusion rules, which are stricter than we need
// for data capture and change over time.
var ninoPattern = regexp.MustCompile(`^[A-Z]{2}[0-9]{6}[A-Z]$`)

// A personal UTR is exactly ten digits.
var utrPattern = regexp.MustCompile(`^[0-9]{10}$`)

// ParseNINO normalises and validates an optional National Insurance number.
// nil/blank -> NULL. Otherwise spaces are stripped and the value upper-cased
// before the shape check, so "sy 59 85 39 d" and "SY598539D" are accepted alike.
func ParseNINO(raw *string) (pgtype.Text, error) {
	if raw == nil {
		return pgtype.Text{Valid: false}, nil
	}
	cleaned := strings.ToUpper(strings.ReplaceAll(strings.TrimSpace(*raw), " ", ""))
	if cleaned == "" {
		return pgtype.Text{Valid: false}, nil
	}
	if !ninoPattern.MatchString(cleaned) {
		return pgtype.Text{}, ErrValidation(
			"national insurance number must be 2 letters, 6 digits and a final letter (e.g. SY598539D)", nil)
	}
	return pgtype.Text{String: cleaned, Valid: true}, nil
}

// ParseUTR normalises and validates an optional personal UTR.
// nil/blank -> NULL. Spaces are stripped; the value must then be exactly 10 digits.
func ParseUTR(raw *string) (pgtype.Text, error) {
	if raw == nil {
		return pgtype.Text{Valid: false}, nil
	}
	cleaned := strings.ReplaceAll(strings.TrimSpace(*raw), " ", "")
	if cleaned == "" {
		return pgtype.Text{Valid: false}, nil
	}
	if !utrPattern.MatchString(cleaned) {
		return pgtype.Text{}, ErrValidation("UTR must be a 10-digit number", nil)
	}
	return pgtype.Text{String: cleaned, Valid: true}, nil
}

// ParseDateOfBirth parses an optional ISO date-of-birth string ("2006-01-02").
// nil/blank -> NULL. A malformed date, a future date, or an absurdly old one
// (before 1900) is rejected. The value is date-only (no time/zone).
func ParseDateOfBirth(raw *string) (pgtype.Date, error) {
	if raw == nil {
		return pgtype.Date{Valid: false}, nil
	}
	s := strings.TrimSpace(*raw)
	if s == "" {
		return pgtype.Date{Valid: false}, nil
	}
	t, err := time.Parse("2006-01-02", s)
	if err != nil {
		return pgtype.Date{}, ErrValidation("date of birth must be a valid date in YYYY-MM-DD format", err)
	}
	// Guard the obvious nonsense: a DOB can't be in the future, and a date before
	// 1900 is almost certainly a typo rather than a living payroll employee.
	today := time.Now().UTC().Truncate(24 * time.Hour)
	if t.After(today) {
		return pgtype.Date{}, ErrValidation("date of birth cannot be in the future", nil)
	}
	if t.Year() < 1900 {
		return pgtype.Date{}, ErrValidation("date of birth is implausibly far in the past", nil)
	}
	return pgtype.Date{Time: t, Valid: true}, nil
}
