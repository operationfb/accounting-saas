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

// VatPeriodResponse is one row in GET /api/v1/vat/periods — a generated VAT return
// period with its filing deadline and a derived display status. `period_key` is a
// synthetic id (the period-end date, YYYY-MM-DD) used to address the return; in
// Phase 2 it becomes HMRC's real periodKey. `label` is the FreeAgent-style "MM YY"
// of the period end (e.g. "05 26"). `display_status` is "Open" while the period is
// still in progress and "Unfiled" once it has ended (later it will merge the saved
// vat_returns filing status — Filed / Marked as filed / …).
type VatPeriodResponse struct {
	PeriodKey     string `json:"period_key"`
	Label         string `json:"label"`
	StartDate     string `json:"start_date"`
	EndDate       string `json:"end_date"`
	DueOn         string `json:"due_on"`
	Ended         bool   `json:"ended"`
	DisplayStatus string `json:"display_status"`
}

// VatReturnResponse is the computed return for one period — GET /api/v1/vat/returns/:periodKey.
// It drives both the Preview (the 9 boxes) and the Full Report (the line lists).
// Boxes 1–5 are VAT amounts (2dp pound strings); boxes 6–9 are net values rounded
// to whole pounds (HMRC convention). `net_due` is the signed Box 5 (negative =
// reclaim/refund); `is_reclaim` is the sign as a bool for the UI.
type VatReturnResponse struct {
	PeriodKey       string `json:"period_key"`
	Label           string `json:"label"`
	StartDate       string `json:"start_date"`
	EndDate         string `json:"end_date"`
	DueOn           string `json:"due_on"`
	DisplayStatus   string `json:"display_status"`
	AccountingBasis string `json:"accounting_basis"`

	Box1 string `json:"box1_vat_due_sales"`
	Box2 string `json:"box2_vat_due_acquisitions"`
	Box3 string `json:"box3_total_vat_due"`
	Box4 string `json:"box4_vat_reclaimed"`
	Box5 string `json:"box5_net_vat"`
	Box6 string `json:"box6_total_sales_ex_vat"`
	Box7 string `json:"box7_total_purchases_ex_vat"`
	Box8 string `json:"box8_ec_dispatches_ex_vat"`
	Box9 string `json:"box9_ec_acquisitions_ex_vat"`

	NetDue    string `json:"net_due"`
	IsReclaim bool   `json:"is_reclaim"`

	SalesLines    []VatReturnLineResponse `json:"sales_lines"`
	PurchaseLines []VatReturnLineResponse `json:"purchase_lines"`
}

// VatReturnLineResponse is one contributing transaction in the Full Report. Money
// is exact 2dp pound strings (the box totals do the whole-pound rounding).
type VatReturnLineResponse struct {
	Date        string `json:"date"`
	Source      string `json:"source"` // invoice | expense | bill | bank
	Description string `json:"description"`
	Reference   string `json:"reference,omitempty"`
	Net         string `json:"net"`
	Vat         string `json:"vat"`
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
