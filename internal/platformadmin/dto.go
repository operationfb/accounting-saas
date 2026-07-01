package platformadmin

// dto.go
// =============================================================================
// Response shapes for the platform-admin ("god view") endpoints
// (GET /api/v1/admin/*). These are READ-ONLY, cross-tenant views a superuser
// uses to browse every organisation and user on the platform. They expose only
// what a support/admin dashboard needs — no secrets (no password hash, tokens,
// last-login IP).
// =============================================================================

// AdminOrganisationResponse is one organisation in the god-view org list.
type AdminOrganisationResponse struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	CountryCode string `json:"country_code"`
	Plan        string `json:"plan"`
	MemberCount int64  `json:"member_count"`
	CreatedAt   string `json:"created_at"` // RFC3339
}

// CreateOrganisationRequest is the JSON body for POST /api/v1/admin/organisations
// (superuser only). Deliberately minimal: name + the two creation-time immutable
// fields (country_code, native_currency). company_type / address / VAT are set
// afterward on the god-view Company Details edit, so they are NOT here. On create
// the Chart of Accounts is provisioned from the country/global template.
type CreateOrganisationRequest struct {
	Name           string `json:"name" binding:"required"`
	CountryCode    string `json:"country_code" binding:"required,len=2"` // ISO 3166-1 alpha-2
	NativeCurrency string `json:"native_currency" binding:"required,len=3"`
}

// AddOrganisationMemberRequest is the body for POST /admin/organisations/:id/members
// (superuser only) — attach an EXISTING platform user to the org. All five roles
// are allowed (the god view is where a new org gets its first owner).
type AddOrganisationMemberRequest struct {
	UserID string `json:"user_id" binding:"required,uuid"`
	Role   string `json:"role" binding:"required,oneof=owner admin member accountant read_only"`
}

// CreateOrganisationUserRequest is the body for POST /admin/organisations/:id/users
// (superuser only) — create a NEW user and attach them to the org. The superuser
// sets an initial password (no email-invite step; that flow stays deferred).
type CreateOrganisationUserRequest struct {
	Email     string `json:"email" binding:"required,email"`
	Password  string `json:"password" binding:"required,min=8"`
	FirstName string `json:"first_name" binding:"required,max=100"`
	LastName  string `json:"last_name" binding:"required,max=100"`
	Role      string `json:"role" binding:"required,oneof=owner admin member accountant read_only"`
}

// AdminUserResponse is one user in the god-view user list.
type AdminUserResponse struct {
	ID          string  `json:"id"`
	Email       string  `json:"email"`
	FirstName   string  `json:"first_name"`
	LastName    string  `json:"last_name"`
	IsActive    bool    `json:"is_active"`
	IsSuperuser bool    `json:"is_superuser"`
	LastLoginAt *string `json:"last_login_at,omitempty"` // RFC3339, nullable
	CreatedAt   string  `json:"created_at"`              // RFC3339
}

// AdminMembershipResponse is one org membership in a user's drill-in (which orgs
// they belong to, and in what role/status) — or, on the org drill-in, one member
// of that org.
type AdminMembershipResponse struct {
	OrganisationID   string `json:"organisation_id"`
	OrganisationName string `json:"organisation_name"`
	Role             string `json:"role"`
	Status           string `json:"status"`
	MemberSince      string `json:"member_since"` // RFC3339
}

// AdminOrganisationMemberResponse is one member row on the org drill-in (a user
// joined to their membership in that org).
type AdminOrganisationMemberResponse struct {
	UserID      string  `json:"user_id"`
	Email       string  `json:"email"`
	FirstName   string  `json:"first_name"`
	LastName    string  `json:"last_name"`
	Role        string  `json:"role"`
	Status      string  `json:"status"`
	LastLoginAt *string `json:"last_login_at,omitempty"` // RFC3339, nullable
	MemberSince string  `json:"member_since"`            // RFC3339
}

// AdminOrganisationDetailResponse is the org drill-in: the org's summary plus its
// members.
type AdminOrganisationDetailResponse struct {
	Organisation AdminOrganisationResponse         `json:"organisation"`
	Members      []AdminOrganisationMemberResponse `json:"members"`
}

// AdminUserDetailResponse is the user drill-in: the user's summary plus every org
// they belong to.
type AdminUserDetailResponse struct {
	User        AdminUserResponse         `json:"user"`
	Memberships []AdminMembershipResponse `json:"memberships"`
}
