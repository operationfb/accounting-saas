package categories

// dto.go
// =============================================================================
// API shapes for the reconcile reference endpoints (the explain "Type" dropdown
// and its per-type category picker). Read-only — no request bodies here.
// =============================================================================

// TransactionTypeResponse is one explanation "Type" option (the FreeAgent
// dropdown). Supported=false marks a future-entity type (Bill/Invoice/Credit
// Note/HP) that isn't explainable yet — the UI shows it disabled.
type TransactionTypeResponse struct {
	Code       string `json:"code"`
	Name       string `json:"name"`
	Direction  string `json:"direction"`   // in | out
	EntityLink string `json:"entity_link"` // NONE | BANK_ACCOUNT | USER | CAPITAL_ASSET | …
	Supported  bool   `json:"supported"`
}

// CategoryResponse is one offered Chart-of-Accounts account for a transaction
// type. Name is the offered label (the mapping's display_label override, else the
// CoA account name). DefaultVat lets the UI pre-select the VAT rate.
type CategoryResponse struct {
	ID          string  `json:"id"`
	NominalCode string  `json:"nominal_code"`
	Name        string  `json:"name"`
	AccountType string  `json:"account_type"`
	ApiGroup    *string `json:"api_group,omitempty"`
	DefaultVat  *string `json:"default_vat,omitempty"` // STANDARD | ZERO | EXEMPT | … (pre-fills VAT)
}
