package organisation

// dto.go
// =============================================================================
// The request/response shapes for the Company Details endpoints (GET/PUT
// /api/v1/organisation), moved here from server.go with the handlers.
// =============================================================================

// UpdateOrganisationRequest is the JSON body accepted by PUT /api/v1/organisation
// (the Company Details screen). It carries the editable company-detail fields.
// Fields the form does not own — slug, native_currency, timezone and (until VAT
// is added) vrn — are deliberately absent: the service preserves them. name is
// the organisation's primary name and is required; everything else is optional
// (a nil pointer → NULL). The owning organisation is taken from the token.
type UpdateOrganisationRequest struct {
	Name        string  `json:"name" binding:"required"`
	LegalName   *string `json:"legal_name"`
	CompanyType *string `json:"company_type" binding:"omitempty,oneof=limited sole_trader partnership landlord corporation"`

	CompaniesHouseNumber    *string `json:"companies_house_number"`
	Utr                     *string `json:"utr"` // "Corporation Tax Reference" on the form
	PayeReference           *string `json:"paye_reference"`
	AccountsOfficeReference *string `json:"accounts_office_reference"`
	// Whether the company claims the Employment Allowance (the payroll EA offset).
	// Defaults to true when the field is omitted (a non-pointer bool with no `binding`
	// tag would be false, which would silently turn off an existing claim — so it's a
	// pointer and the service treats nil as "leave unchanged / default true").
	ClaimsEmploymentAllowance *bool `json:"claims_employment_allowance"`

	AddressLine1 *string `json:"address_line_1"`
	AddressLine2 *string `json:"address_line_2"`
	AddressLine3 *string `json:"address_line_3"`
	Town         *string `json:"town"`
	Region       *string `json:"region"`
	Postcode     *string `json:"postcode"`
	// country_code and native_currency are deliberately ABSENT: both are fixed at
	// organisation creation and immutable here. The service preserves the stored
	// values (an old client that still sends country_code is simply ignored — that
	// is the guard). They are still returned read-only on the GET response.

	BusinessPhone *string `json:"business_phone"`
	ContactEmail  *string `json:"contact_email" binding:"omitempty,email"`
	ContactPhone  *string `json:"contact_phone"`
	Website       *string `json:"website"`

	BusinessCategory    *string `json:"business_category"`
	BusinessDescription *string `json:"business_description"`
}

// OrganisationDetailsResponse is the JSON returned by GET/PUT /api/v1/organisation.
// Nullable columns are omitempty pointers; the never-null fields are plain values.
// The legacy free-text registered_address is not exposed (superseded by the
// structured address). native_currency / timezone / plan / is_active are returned
// read-only for the frontend even though this screen doesn't edit them.
type OrganisationDetailsResponse struct {
	ID          string  `json:"id"`
	Name        string  `json:"name"`
	Slug        *string `json:"slug,omitempty"`
	LegalName   *string `json:"legal_name,omitempty"`
	CompanyType *string `json:"company_type,omitempty"`

	CompaniesHouseNumber      *string `json:"companies_house_number,omitempty"`
	Utr                       *string `json:"utr,omitempty"`
	Vrn                       *string `json:"vrn,omitempty"`
	PayeReference             *string `json:"paye_reference,omitempty"`
	AccountsOfficeReference   *string `json:"accounts_office_reference,omitempty"`
	ClaimsEmploymentAllowance bool    `json:"claims_employment_allowance"`

	AddressLine1 *string `json:"address_line_1,omitempty"`
	AddressLine2 *string `json:"address_line_2,omitempty"`
	AddressLine3 *string `json:"address_line_3,omitempty"`
	Town         *string `json:"town,omitempty"`
	Region       *string `json:"region,omitempty"`
	Postcode     *string `json:"postcode,omitempty"`
	CountryCode  string  `json:"country_code"`

	BusinessPhone *string `json:"business_phone,omitempty"`
	ContactEmail  *string `json:"contact_email,omitempty"`
	ContactPhone  *string `json:"contact_phone,omitempty"`
	Website       *string `json:"website,omitempty"`

	BusinessCategory    *string `json:"business_category,omitempty"`
	BusinessDescription *string `json:"business_description,omitempty"`

	NativeCurrency string `json:"native_currency"`
	Timezone       string `json:"timezone"`
	Plan           string `json:"plan"`
	IsActive       bool   `json:"is_active"`
	CreatedAt      string `json:"created_at"`
	UpdatedAt      string `json:"updated_at"`
}
