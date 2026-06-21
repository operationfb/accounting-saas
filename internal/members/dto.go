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
