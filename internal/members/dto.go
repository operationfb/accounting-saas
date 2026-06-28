package members

// dto.go
// =============================================================================
// The response shape for the members endpoint (GET /api/v1/members), moved here
// from server.go with the handler.
// =============================================================================

// MemberResponse is the JSON returned for one organisation member (a membership
// joined to its user). It deliberately exposes only what a "Team / Manage users"
// screen needs — no password hash or other secrets. UUIDs are strings and
// timestamps are RFC3339; avatar_url and last_login_at are nullable (omitted when
// absent). role and status are the membership enum/status values, so the UI can
// badge each member.
type MemberResponse struct {
	MembershipID string  `json:"membership_id"`
	UserID       string  `json:"user_id"`
	Email        string  `json:"email"`
	FirstName    string  `json:"first_name"`
	LastName     string  `json:"last_name"`
	Role         string  `json:"role"`   // owner | admin | member | accountant | read_only
	Status       string  `json:"status"` // active | invited | suspended | deactivated
	AvatarURL    *string `json:"avatar_url,omitempty"`
	MemberSince  string  `json:"member_since"` // RFC3339 (membership created_at)
	LastLoginAt  *string `json:"last_login_at,omitempty"`
}

// MemberDetailResponse is the JSON for ONE member on the admin User Details screen
// (GET /api/v1/members/:id). It is the list shape plus the payroll-identity fields
// the detail form edits (national_insurance_number / utr / date_of_birth). Those
// come from the users row, which the list query doesn't select — hence a separate,
// richer response. Still no secrets (no password hash, tokens, last-login IP).
type MemberDetailResponse struct {
	MembershipID            string  `json:"membership_id"`
	UserID                  string  `json:"user_id"`
	Email                   string  `json:"email"`
	FirstName               string  `json:"first_name"`
	LastName                string  `json:"last_name"`
	Role                    string  `json:"role"`
	Status                  string  `json:"status"`
	AvatarURL               *string `json:"avatar_url,omitempty"`
	NationalInsuranceNumber *string `json:"national_insurance_number,omitempty"`
	UTR                     *string `json:"utr,omitempty"`
	DateOfBirth             *string `json:"date_of_birth,omitempty"` // ISO YYYY-MM-DD
	MemberSince             string  `json:"member_since"`            // RFC3339 (membership created_at)
	LastLoginAt             *string `json:"last_login_at,omitempty"`
}

// UpdateMemberRequest is the body for PUT /api/v1/members/:id — an owner/admin
// editing another user's details, role and status. Names are required (NOT NULL
// columns); the payroll fields are optional pointers (blank/omitted -> NULL) and
// validated via the shared kernel.Parse* helpers. role/status carry oneof binding
// so an unknown value is a 400 at the edge; the service adds the cross-cutting
// guards (self lock-out, owner-only owner role). status excludes 'invited', which
// is owned by the (deferred) invite flow, not this form.
type UpdateMemberRequest struct {
	FirstName               string  `json:"first_name" binding:"required,max=100"`
	LastName                string  `json:"last_name" binding:"required,max=100"`
	NationalInsuranceNumber *string `json:"national_insurance_number"`
	UTR                     *string `json:"utr"`
	DateOfBirth             *string `json:"date_of_birth"` // ISO YYYY-MM-DD
	Role                    string  `json:"role" binding:"required,oneof=owner admin member accountant read_only"`
	Status                  string  `json:"status" binding:"required,oneof=active suspended deactivated"`
}
