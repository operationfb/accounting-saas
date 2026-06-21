package contacts

// dto.go
// =============================================================================
// The request/response shapes for the contacts endpoints (/api/v1/contacts*),
// moved here from server.go with the handlers.
// =============================================================================

// CreateContactRequest is the JSON body accepted by POST /api/v1/contacts.
// Almost every field is optional (pointer = absent → NULL), matching the form's
// permissive shape: a contact may be a person, a company, or both. The owning
// organisation and creator are taken from the token, never the body.
//
// Notes:
//   - charge_vat is validated by `oneof`; omitted → service default SAME_COUNTRY.
//   - country_code omitted → service default GB (and it is upper-cased).
//   - display_contact_name is a *bool so an omitted value can default to TRUE
//     (the form's checked-by-default box) rather than Go's zero value false.
//   - default_payment_terms_days is a *int32 so 0 ("Due on Receipt") is distinct
//     from absent (no contact-level terms → NULL).
type CreateContactRequest struct {
	FirstName        *string `json:"first_name"`
	LastName         *string `json:"last_name"`
	OrganisationName *string `json:"organisation_name"`
	Email            *string `json:"email"         binding:"omitempty,email"`
	BillingEmail     *string `json:"billing_email" binding:"omitempty,email"`
	Telephone        *string `json:"telephone"`
	Mobile           *string `json:"mobile"`

	AddressLine1 *string `json:"address_line_1"`
	AddressLine2 *string `json:"address_line_2"`
	AddressLine3 *string `json:"address_line_3"`
	Town         *string `json:"town"`
	Region       *string `json:"region"`
	Postcode     *string `json:"postcode"`
	CountryCode  string  `json:"country_code" binding:"omitempty,len=2"` // ISO 3166-1 alpha-2; defaults to GB

	DefaultPaymentTermsDays         *int32  `json:"default_payment_terms_days" binding:"omitempty,min=0"`
	UsesContactLevelEmailSettings   bool    `json:"uses_contact_level_email_settings"`
	UsesContactLevelInvoiceSequence bool    `json:"uses_contact_level_invoice_sequence"`
	DisplayContactName              *bool   `json:"display_contact_name"` // nil → default TRUE
	ChargeVAT                       string  `json:"charge_vat" binding:"omitempty,oneof=ALWAYS NEVER SAME_COUNTRY"`
	VATRegistrationNumber           *string `json:"vat_registration_number"`
	InvoiceLanguage                 string  `json:"invoice_language"` // defaults to "en"

	BankSortCode      *string `json:"bank_sort_code"`
	BankAccountNumber *string `json:"bank_account_number"`
	BankRecipientName *string `json:"bank_recipient_name"`
}

// UpdateContactRequest is the JSON body accepted by PUT /api/v1/contacts/:id.
// It mirrors CreateContactRequest's editable fields — PUT is a full replace of
// the editable representation. organisation_id and created_by are never read
// from the body.
type UpdateContactRequest struct {
	FirstName        *string `json:"first_name"`
	LastName         *string `json:"last_name"`
	OrganisationName *string `json:"organisation_name"`
	Email            *string `json:"email"         binding:"omitempty,email"`
	BillingEmail     *string `json:"billing_email" binding:"omitempty,email"`
	Telephone        *string `json:"telephone"`
	Mobile           *string `json:"mobile"`

	AddressLine1 *string `json:"address_line_1"`
	AddressLine2 *string `json:"address_line_2"`
	AddressLine3 *string `json:"address_line_3"`
	Town         *string `json:"town"`
	Region       *string `json:"region"`
	Postcode     *string `json:"postcode"`
	CountryCode  string  `json:"country_code" binding:"omitempty,len=2"`

	DefaultPaymentTermsDays         *int32  `json:"default_payment_terms_days" binding:"omitempty,min=0"`
	UsesContactLevelEmailSettings   bool    `json:"uses_contact_level_email_settings"`
	UsesContactLevelInvoiceSequence bool    `json:"uses_contact_level_invoice_sequence"`
	DisplayContactName              *bool   `json:"display_contact_name"`
	ChargeVAT                       string  `json:"charge_vat" binding:"omitempty,oneof=ALWAYS NEVER SAME_COUNTRY"`
	VATRegistrationNumber           *string `json:"vat_registration_number"`
	InvoiceLanguage                 string  `json:"invoice_language"`

	BankSortCode      *string `json:"bank_sort_code"`
	BankAccountNumber *string `json:"bank_account_number"`
	BankRecipientName *string `json:"bank_recipient_name"`
}

// ContactResponse is the JSON returned for a created/fetched/updated contact.
// Nullable columns are returned as omitempty pointers; the never-null
// invoicing-option fields are returned as plain values.
type ContactResponse struct {
	ID              string `json:"id"`
	OrganisationID  string `json:"organisation_id"`
	CreatedByUserID string `json:"created_by_user_id"`

	FirstName        *string `json:"first_name,omitempty"`
	LastName         *string `json:"last_name,omitempty"`
	OrganisationName *string `json:"organisation_name,omitempty"`
	Email            *string `json:"email,omitempty"`
	BillingEmail     *string `json:"billing_email,omitempty"`
	Telephone        *string `json:"telephone,omitempty"`
	Mobile           *string `json:"mobile,omitempty"`

	AddressLine1 *string `json:"address_line_1,omitempty"`
	AddressLine2 *string `json:"address_line_2,omitempty"`
	AddressLine3 *string `json:"address_line_3,omitempty"`
	Town         *string `json:"town,omitempty"`
	Region       *string `json:"region,omitempty"`
	Postcode     *string `json:"postcode,omitempty"`
	CountryCode  string  `json:"country_code"`

	DefaultPaymentTermsDays         *int32  `json:"default_payment_terms_days,omitempty"`
	UsesContactLevelEmailSettings   bool    `json:"uses_contact_level_email_settings"`
	UsesContactLevelInvoiceSequence bool    `json:"uses_contact_level_invoice_sequence"`
	DisplayContactName              bool    `json:"display_contact_name"`
	ChargeVAT                       string  `json:"charge_vat"`
	VATRegistrationNumber           *string `json:"vat_registration_number,omitempty"`
	InvoiceLanguage                 string  `json:"invoice_language"`

	BankSortCode      *string `json:"bank_sort_code,omitempty"`
	BankAccountNumber *string `json:"bank_account_number,omitempty"`
	BankRecipientName *string `json:"bank_recipient_name,omitempty"`

	IsActive  bool   `json:"is_active"`
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
}
