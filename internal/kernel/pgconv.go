package kernel

// pgconv.go (package kernel)
// =============================================================================
// The widely-shared pgtype <-> Go conversion helpers. Only the helpers used
// across MULTIPLE domains live here (NullText, NullTextToPtr, NullInt32,
// Int32FromPtr); domain-local conversions stay with their domain and migrate
// with it.
//
// A note on the two int helpers, because the difference matters:
//   - NullInt32 maps 0 -> NULL (right for e.g. vat_rate_bps, where 0 means
//     "no rate").
//   - Int32FromPtr PRESERVES 0 (right for e.g. default_payment_terms_days, where
//     0 means "Due on Receipt"); only a nil pointer becomes NULL.
// =============================================================================

import "github.com/jackc/pgx/v5/pgtype"

// NullText converts a *string to pgtype.Text.
// nil pointer -> NULL in the database; non-nil pointer -> the string value.
func NullText(s *string) pgtype.Text {
	if s == nil {
		return pgtype.Text{Valid: false}
	}
	return pgtype.Text{String: *s, Valid: true}
}

// NullTextToPtr is the reverse: pgtype.Text -> *string.
// Invalid (NULL) -> nil; Valid -> pointer to the string.
func NullTextToPtr(t pgtype.Text) *string {
	if !t.Valid {
		return nil
	}
	return &t.String
}

// NullInt32 wraps an int32 in pgtype.Int4, mapping 0 -> NULL.
// Used for nullable integer columns where 0 is not a meaningful value
// (e.g. vat_rate_bps when there is no VAT). Do NOT use where 0 is meaningful —
// use Int32FromPtr for those.
func NullInt32(n int32) pgtype.Int4 {
	return pgtype.Int4{Int32: n, Valid: n != 0}
}

// Int32FromPtr converts a *int32 to pgtype.Int4 PRESERVING 0.
// nil pointer -> NULL; non-nil pointer -> its value (including 0).
func Int32FromPtr(n *int32) pgtype.Int4 {
	if n == nil {
		return pgtype.Int4{Valid: false}
	}
	return pgtype.Int4{Int32: *n, Valid: true}
}
