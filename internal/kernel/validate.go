package kernel

// validate.go
// =============================================================================
// Small shared validation/normalisation helpers — defence-in-depth behind the
// handlers' binding tags and the DB CHECK constraints, so a service is correct
// even when called directly (e.g. from a test). Kept in the kernel because they
// are reused across domains (e.g. contacts + organisation both validate a
// country code).
// =============================================================================

import "strings"

// NormaliseCountryCode upper-cases + trims the country code and defaults it to
// 'GB' when blank. It must be exactly two letters (matches the CHAR(2) column and
// the uppercase CHECK). Binding also enforces len=2, but we re-check after
// trimming so a service-level caller (e.g. a test) is validated too.
func NormaliseCountryCode(raw string) (string, error) {
	code := strings.ToUpper(strings.TrimSpace(raw))
	if code == "" {
		return "GB", nil
	}
	if len(code) != 2 {
		return "", ErrValidation("country_code must be a 2-letter ISO 3166-1 alpha-2 code", nil)
	}
	return code, nil
}
