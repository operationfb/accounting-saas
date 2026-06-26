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
// is exact 2dp pound strings (the box totals do the whole-pound rounding). ID is the
// underlying record's UUID — present for expense/invoice/bill lines (so the SPA can
// link each to its detail view), omitted for bank/cash lines that have no single
// addressable record.
type VatReturnLineResponse struct {
	ID          string `json:"id,omitempty"`
	Date        string `json:"date"`
	Source      string `json:"source"` // invoice | expense | bill | bank
	Description string `json:"description"`
	Reference   string `json:"reference,omitempty"`
	Net         string `json:"net"`
	Vat         string `json:"vat"`
}

// VatSettingsResponse is the JSON returned by GET/PUT /api/v1/vat/settings.
// Nullable columns are omitempty pointers; the toggles are plain bools.
// HMRCConnected reflects whether this org has an active HMRC MTD connection
// (from the integrations table) — used by the SPA to enable the Submit button.
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

	HMRCConnected   bool    `json:"hmrc_connected"`
	HMRCConnectedAt *string `json:"hmrc_connected_at,omitempty"`
}

// VatSubmitResponse is returned by POST /api/v1/vat/returns/:periodKey/submit
// after a successful HMRC MTD submission. The form_bundle_number is the HMRC
// reference the taxpayer should keep; charge_ref_number is present when HMRC
// issues a payment charge.
type VatSubmitResponse struct {
	PeriodKey        string  `json:"period_key"`
	FormBundleNumber string  `json:"form_bundle_number"`
	ProcessingDate   string  `json:"processing_date"`
	ChargeRefNumber  *string `json:"charge_ref_number,omitempty"`
}

// VatPeriodSettings is the (frequency, first-period-end, effective-date) triple
// that determines the generated VAT period schedule — the subset of settings the
// reconciliation check compares and would rewrite. Dates are YYYY-MM-DD strings.
type VatPeriodSettings struct {
	ReturnFrequency      string `json:"return_frequency,omitempty"`
	FirstReturnPeriodEnd string `json:"first_return_period_end,omitempty"`
	EffectiveDate        string `json:"effective_date,omitempty"`
}

// VatPeriodCheckResponse is returned by GET /api/v1/vat/hmrc/period-check — whether
// the org's locally-generated VAT periods line up with HMRC's obligations, and the
// settings that would make them match. `applicable` is false when there's nothing to
// reconcile (not connected, no VRN, no obligations, or an HMRC error — fail open).
// `filed_periods_affected` is how many already-saved returns would no longer appear
// after the rewrite (a warning, not a blocker).
type VatPeriodCheckResponse struct {
	Applicable           bool              `json:"applicable"`
	Matches              bool              `json:"matches"`
	Current              VatPeriodSettings `json:"current"`
	Suggested            VatPeriodSettings `json:"suggested"`
	FiledPeriodsAffected int               `json:"filed_periods_affected"`
}

// =============================================================================
// HMRC VAT-account dashboard DTOs (the read layer over the MTD VAT GET APIs).
// These mirror HMRC's own resources but in our boundary conventions: money is
// 2-dp pound STRINGS (never float), dates are YYYY-MM-DD strings, and fields HMRC
// omits when absent are omitempty pointers. The service (account.go) fills them
// from the hmrc* structs in hmrc.go. See internal/vat/account.go.
// =============================================================================

// HMRCObligationResponse is one VAT obligation (a return period) from
// GET /organisations/vat/{vrn}/obligations. Status is "O" (open) or "F" (fulfilled);
// received is the filed date, present only when fulfilled.
type HMRCObligationResponse struct {
	PeriodKey string  `json:"period_key"`
	Start     string  `json:"start"`
	End       string  `json:"end"`
	Due       string  `json:"due"`
	Status    string  `json:"status"`
	Received  *string `json:"received,omitempty"`
}

// HMRCReturnResponse is HMRC's view of a submitted return (the 9 boxes) from
// GET /organisations/vat/{vrn}/returns/{periodKey}. Boxes 1–5 are 2-dp pound
// strings; boxes 6–9 are whole-pound strings (HMRC's own convention).
type HMRCReturnResponse struct {
	PeriodKey string `json:"period_key"`
	Box1      string `json:"box1_vat_due_sales"`
	Box2      string `json:"box2_vat_due_acquisitions"`
	Box3      string `json:"box3_total_vat_due"`
	Box4      string `json:"box4_vat_reclaimed"`
	Box5      string `json:"box5_net_vat"`
	Box6      string `json:"box6_total_sales_ex_vat"`
	Box7      string `json:"box7_total_purchases_ex_vat"`
	Box8      string `json:"box8_ec_dispatches_ex_vat"`
	Box9      string `json:"box9_ec_acquisitions_ex_vat"`
}

// HMRCLiabilityResponse is one amount owed to HMRC from
// GET /organisations/vat/{vrn}/liabilities — the dashboard's "what you owe" card.
// from/to bound the tax period the liability is for.
type HMRCLiabilityResponse struct {
	Type              string  `json:"type"`
	From              *string `json:"from,omitempty"`
	To                *string `json:"to,omitempty"`
	OriginalAmount    string  `json:"original_amount"`
	OutstandingAmount string  `json:"outstanding_amount"`
	Due               *string `json:"due,omitempty"`
}

// HMRCPaymentResponse is one payment received by HMRC from
// GET /organisations/vat/{vrn}/payments — the "payments to HMRC" card.
type HMRCPaymentResponse struct {
	Amount   string  `json:"amount"`
	Received *string `json:"received,omitempty"`
}

// HMRCPenaltiesResponse summarises GET /organisations/vat/{vrn}/penalties — the
// late-submission points meter (active/inactive vs threshold), the running total of
// penalty charges, and the individual charges (each with charge_reference for the
// financial-details drill-down).
type HMRCPenaltiesResponse struct {
	ActivePoints   int                         `json:"active_points"`
	InactivePoints int                         `json:"inactive_points"`
	Threshold      int                         `json:"threshold"`
	TotalPenalties string                      `json:"total_penalties"`
	Penalties      []HMRCPenaltyChargeResponse `json:"penalties"`
}

// HMRCPenaltyChargeResponse is one penalty charge. Type is "late_submission" or
// "late_payment".
type HMRCPenaltyChargeResponse struct {
	Type            string `json:"type"`
	Category        string `json:"category,omitempty"`
	ChargeReference string `json:"charge_reference,omitempty"`
	Status          string `json:"status,omitempty"`
	Amount          string `json:"amount"`
}

// HMRCFinancialDetailsResponse is the charge breakdown for one penalty from
// GET /organisations/vat/{vrn}/financial-details/{penaltyChargeReference}.
type HMRCFinancialDetailsResponse struct {
	ChargeReference string                     `json:"charge_reference"`
	Documents       []HMRCFinancialDocResponse `json:"documents"`
}

// HMRCFinancialDocResponse is one document line in a financial-details response.
type HMRCFinancialDocResponse struct {
	Type              string  `json:"type,omitempty"`
	ChargeReference   string  `json:"charge_reference,omitempty"`
	TotalAmount       string  `json:"total_amount"`
	OutstandingAmount string  `json:"outstanding_amount"`
	DueDate           *string `json:"due_date,omitempty"`
}

// HMRCInformationResponse is the registered VAT business details from
// GET /organisations/vat/{vrn}/information — the "registration details" card.
type HMRCInformationResponse struct {
	BusinessName     string   `json:"business_name,omitempty"`
	TradingName      string   `json:"trading_name,omitempty"`
	AddressLines     []string `json:"address_lines,omitempty"`
	Postcode         string   `json:"postcode,omitempty"`
	CountryCode      string   `json:"country_code,omitempty"`
	RegistrationDate *string  `json:"registration_date,omitempty"`
}
