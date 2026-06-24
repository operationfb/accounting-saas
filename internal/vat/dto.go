package vat

// dto.go
// =============================================================================
// Request/response shapes for the VAT Registration settings endpoints
// (GET/PUT /api/v1/vat/settings) — the "UK VAT Registration" screen, modelled on
// FreeAgent's. These settings live as columns on the organisations table (so the
// service is a thin layer over the auth queries, like the Company Details and My
// Details screens), and the VAT-return calculation engine reads them later.
//
// Conventions matching the rest of the SPA boundary:
//   - dates cross as YYYY-MM-DD strings (nil/omitted = not set),
//   - the flat-rate percentage crosses as a percentage STRING ("10.5") and is
//     stored as basis points (the bps convention used for every rate),
//   - nullable fields are pointers; never-null toggles are plain bools.
// =============================================================================

// VatSettingsRequest is the JSON body for PUT /api/v1/vat/settings.
//
// vat_registered is the master switch: when false the remaining fields are
// optional and ignored. When true, the certificate fields (vrn, the two dates,
// frequency, accounting basis) are required — enforced in the service to match
// the form's required markers, on top of the binding `oneof` tags here.
type VatSettingsRequest struct {
	// "Are you VAT Registered?"
	VatRegistered bool `json:"vat_registered"`

	// VAT Registration Number — bare 9 digits (no GB prefix). Required when registered.
	Vrn *string `json:"vrn"`

	// "Do you need to use VAT rates other than standard UK ones?"
	UsesNonStandardRates bool `json:"uses_non_standard_rates"`

	// Dates from the VAT registration certificate (YYYY-MM-DD).
	EffectiveDate        *string `json:"effective_date"`
	FirstReturnPeriodEnd *string `json:"first_return_period_end"`

	// "Frequency of returns" and "VAT Accounting Basis".
	ReturnFrequency *string `json:"return_frequency" binding:"omitempty,oneof=monthly quarterly annually"`
	AccountingBasis *string `json:"accounting_basis" binding:"omitempty,oneof=invoice cash"`

	// "Are you on the Flat Rate Scheme?" + the flat-rate percentage ("10.5"),
	// stored as basis points. The flat-rate calculation itself is deferred.
	FlatRateScheme     bool    `json:"flat_rate_scheme"`
	FlatRatePercentage *string `json:"flat_rate_percentage"`

	// "Include pre-registration expenses from", in months: 6 (services) or
	// 48 (goods, 4 years); nil = don't include. Inclusion math is deferred.
	PreRegExpenseMonths *int32 `json:"pre_reg_expense_months"`
}

// VatSettingsResponse is the JSON returned by GET/PUT /api/v1/vat/settings.
// Nullable columns are omitempty pointers; the toggles are plain bools.
type VatSettingsResponse struct {
	VatRegistered        bool    `json:"vat_registered"`
	Vrn                  *string `json:"vrn,omitempty"`
	UsesNonStandardRates bool    `json:"uses_non_standard_rates"`
	EffectiveDate        *string `json:"effective_date,omitempty"`
	FirstReturnPeriodEnd *string `json:"first_return_period_end,omitempty"`
	ReturnFrequency      *string `json:"return_frequency,omitempty"`
	AccountingBasis      *string `json:"accounting_basis,omitempty"`
	FlatRateScheme       bool    `json:"flat_rate_scheme"`
	FlatRatePercentage   *string `json:"flat_rate_percentage,omitempty"`
	PreRegExpenseMonths  *int32  `json:"pre_reg_expense_months,omitempty"`
}
