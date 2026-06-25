package vat

// service.go
// =============================================================================
// Service — the business logic for an organisation's VAT registration settings
// (the "UK VAT Registration" screen). Like the Company Details (organisation) and
// My Details (userauth) screens, all of this data lives on an existing table
// (organisations), so this is a thin layer over the shared auth queries:
//
//   HTTP handler → Service (this file) → auth.Querier (db/auth) → PostgreSQL
//
// There is always exactly ONE organisation in scope — the caller's, from the
// bearer token — so there is no id to pass and multi-tenant isolation is inherent.
//
// Access rules (mirroring Company Details):
//   - GET : any ACTIVE member may view the settings.
//   - PUT : only an OWNER or ADMIN may edit them (kernel.IsOrgAdmin).
//
// The PUT uses the focused UpdateOrganisationVatSettings query, which writes ONLY
// the VAT columns — so it can never wipe the Company Details fields, and vice
// versa. (This is why vrn is edited here, not on Company Details, which preserves
// it.) Future screens — the period list, the return preview/full-report — will be
// added to this same package alongside the calculation engine.
// =============================================================================

import (
	"context"
	"errors"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	auth "github.com/operationfb/accounting-saas/db/auth"
	vatdb "github.com/operationfb/accounting-saas/db/vat"
	"github.com/operationfb/accounting-saas/internal/kernel"
	"github.com/operationfb/accounting-saas/money"
)

// The allowed enum sets — defence-in-depth behind the handler's `oneof` binding
// and the DB CHECK constraints, so the service is correct when called directly
// (e.g. from a test).
var (
	validReturnFrequency = map[string]bool{"monthly": true, "quarterly": true, "annually": true}
	validAccountingBasis = map[string]bool{"invoice": true, "cash": true}
)

// vrnDigits matches a bare 9-digit VRN — exactly what HMRC's MTD API expects.
var vrnDigits = regexp.MustCompile(`^\d{9}$`)

// Service holds the auth query set (VAT settings live on the organisations table)
// plus the cross-domain VAT read queries (db/vat) used by the calculation engine.
// All reads are single-statement, so there is no pool/transaction to keep.
type Service struct {
	authQueries auth.Querier
	queries     *vatdb.Queries
}

// NewService is the constructor, called once in main.go. authQueries is the shared
// auth.Querier; queries is the VAT read-query set (db/vat) used by GetReturn.
func NewService(authQueries auth.Querier, queries *vatdb.Queries) *Service {
	return &Service{authQueries: authQueries, queries: queries}
}

// authorize confirms the caller is an ACTIVE member and returns their role
// (the role lets UpdateSettings gate editing to owners/admins).
func (s *Service) authorize(ctx context.Context, userID, orgID uuid.UUID) (auth.OrganisationRole, error) {
	return kernel.AuthorizeMember(ctx, s.authQueries, userID, orgID)
}

// =============================================================================
// GET
// =============================================================================

// GetSettings returns the caller's organisation's VAT settings. Any active
// member may read; the org is taken from the token.
func (s *Service) GetSettings(ctx context.Context, authUserID, authOrgID uuid.UUID) (*VatSettingsResponse, error) {
	if _, err := s.authorize(ctx, authUserID, authOrgID); err != nil {
		return nil, err
	}
	org, err := s.authQueries.GetOrganisation(ctx, authOrgID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, kernel.ErrNotFound("organisation", authOrgID.String())
		}
		return nil, kernel.ErrInternal(err)
	}
	return vatSettingsToResponse(org), nil
}

// =============================================================================
// UPDATE
// =============================================================================

// UpdateSettings updates the VAT settings (PUT semantics). Owner/admin only; the
// org is taken from the token. Writes only the VAT columns (the focused query),
// so the Company Details fields are untouched.
func (s *Service) UpdateSettings(ctx context.Context, authUserID, authOrgID uuid.UUID, req VatSettingsRequest) (*VatSettingsResponse, error) {
	role, err := s.authorize(ctx, authUserID, authOrgID)
	if err != nil {
		return nil, err
	}
	if !kernel.IsOrgAdmin(role) {
		return nil, kernel.ErrForbidden("only owners and admins can edit VAT settings")
	}

	// Normalise / validate each field. Each helper treats nil/blank as "not set"
	// (NULL) and only rejects a present-but-invalid value.
	vrn, err := normaliseVRN(req.Vrn)
	if err != nil {
		return nil, err
	}
	effective, err := parseDatePtr(req.EffectiveDate, "effective_date")
	if err != nil {
		return nil, err
	}
	firstEnd, err := parseDatePtr(req.FirstReturnPeriodEnd, "first_return_period_end")
	if err != nil {
		return nil, err
	}
	freq, err := normaliseEnum(req.ReturnFrequency, validReturnFrequency, "return_frequency", "monthly, quarterly, annually")
	if err != nil {
		return nil, err
	}
	basis, err := normaliseEnum(req.AccountingBasis, validAccountingBasis, "accounting_basis", "invoice, cash")
	if err != nil {
		return nil, err
	}
	flatBps, err := flatRateToBps(req.FlatRateScheme, req.FlatRatePercentage)
	if err != nil {
		return nil, err
	}

	// When registered, the certificate fields are required (matches the form's
	// required markers). When not registered we don't force them — a business can
	// flip the switch off and save without re-entering a certificate.
	if req.VatRegistered {
		switch {
		case !vrn.Valid:
			return nil, kernel.ErrValidation("vrn is required when VAT registered (9 digits)", nil)
		case !effective.Valid:
			return nil, kernel.ErrValidation("effective_date is required when VAT registered", nil)
		case !firstEnd.Valid:
			return nil, kernel.ErrValidation("first_return_period_end is required when VAT registered", nil)
		case !freq.Valid:
			return nil, kernel.ErrValidation("return_frequency is required when VAT registered", nil)
		case !basis.Valid:
			return nil, kernel.ErrValidation("accounting_basis is required when VAT registered", nil)
		}
	}

	updated, err := s.authQueries.UpdateOrganisationVatSettings(ctx, auth.UpdateOrganisationVatSettingsParams{
		ID:                      authOrgID,
		Vrn:                     vrn,
		VatRegistered:           req.VatRegistered,
		VatUsesNonStandardRates: req.UsesNonStandardRates,
		VatEffectiveDate:        effective,
		VatFirstReturnPeriodEnd: firstEnd,
		VatReturnFrequency:      freq,
		VatAccountingBasis:      basis,
		VatFlatRateScheme:       req.FlatRateScheme,
		VatFlatRateBps:          flatBps,
		VatPreRegExpenseMonths:  kernel.Int32FromPtr(req.PreRegExpenseMonths),
	})
	if err != nil {
		// The row was live a moment ago; ErrNoRows means it was soft-deleted in between.
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, kernel.ErrNotFound("organisation", authOrgID.String())
		}
		return nil, kernel.ErrInternal(err)
	}
	return vatSettingsToResponse(updated), nil
}

// =============================================================================
// PERIODS
// =============================================================================

// ListPeriods returns the org's VAT return periods, generated locally from its
// settings (effective date / first-return end / frequency). Any active member may
// read. The list is newest-first (most recent period at the top, like FreeAgent).
// It is empty when the org is not VAT-registered or its settings are incomplete —
// the frontend then points the user at the VAT Registration screen.
func (s *Service) ListPeriods(ctx context.Context, authUserID, authOrgID uuid.UUID) ([]VatPeriodResponse, error) {
	if _, err := s.authorize(ctx, authUserID, authOrgID); err != nil {
		return nil, err
	}
	org, err := s.authQueries.GetOrganisation(ctx, authOrgID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, kernel.ErrNotFound("organisation", authOrgID.String())
		}
		return nil, kernel.ErrInternal(err)
	}

	// Can't generate a schedule without the certificate settings.
	if !org.VatRegistered || !org.VatEffectiveDate.Valid ||
		!org.VatFirstReturnPeriodEnd.Valid || !org.VatReturnFrequency.Valid {
		return []VatPeriodResponse{}, nil
	}

	today := dateOnlyUTC(time.Now().UTC())
	periods := generateVATPeriods(
		org.VatEffectiveDate.Time,
		org.VatFirstReturnPeriodEnd.Time,
		today,
		org.VatReturnFrequency.String,
	)

	// Map to DTOs, reversing to newest-first.
	out := make([]VatPeriodResponse, 0, len(periods))
	for i := len(periods) - 1; i >= 0; i-- {
		p := periods[i]
		ended := p.End.Before(today)
		status := "Open"
		if ended {
			status = "Unfiled"
		}
		out = append(out, VatPeriodResponse{
			PeriodKey:     p.End.Format("2006-01-02"),
			Label:         p.End.Format("01 06"), // "MM YY", e.g. "05 26"
			StartDate:     p.Start.Format("2006-01-02"),
			EndDate:       p.End.Format("2006-01-02"),
			DueOn:         p.Due.Format("2006-01-02"),
			Ended:         ended,
			DisplayStatus: status,
		})
	}
	return out, nil
}

// =============================================================================
// RETURN (computed)
// =============================================================================

// GetReturn computes the VAT return for one period — identified by its period-end
// key (the period-end date, e.g. "2026-05-31") — and returns the 9 boxes plus the
// contributing lines (driving the Preview + Full Report). Any active member may
// read. The period is resolved from the settings-generated schedule; an unknown key
// (or a not-registered / incompletely-configured org) is 404.
//
// v1 computes the INVOICE/ACCRUAL basis: the documents (invoices SENT, bills,
// expenses APPROVED/PAID) by date + direct-category bank explanations. It reports
// the org's configured basis in the response, but the cash-basis computation is the
// next slice — until then the figures are accrual regardless of the setting.
func (s *Service) GetReturn(ctx context.Context, authUserID, authOrgID uuid.UUID, periodKey string) (*VatReturnResponse, error) {
	if _, err := s.authorize(ctx, authUserID, authOrgID); err != nil {
		return nil, err
	}
	org, err := s.authQueries.GetOrganisation(ctx, authOrgID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, kernel.ErrNotFound("organisation", authOrgID.String())
		}
		return nil, kernel.ErrInternal(err)
	}
	if !org.VatRegistered || !org.VatEffectiveDate.Valid ||
		!org.VatFirstReturnPeriodEnd.Valid || !org.VatReturnFrequency.Valid {
		return nil, kernel.ErrNotFound("vat return period", periodKey)
	}

	today := dateOnlyUTC(time.Now().UTC())
	periods := generateVATPeriods(
		org.VatEffectiveDate.Time,
		org.VatFirstReturnPeriodEnd.Time,
		today,
		org.VatReturnFrequency.String,
	)
	var period *vatPeriod
	for i := range periods {
		if periods[i].End.Format("2006-01-02") == periodKey {
			period = &periods[i]
			break
		}
	}
	if period == nil {
		return nil, kernel.ErrNotFound("vat return period", periodKey)
	}

	from := pgtype.Date{Time: period.Start, Valid: true}
	to := pgtype.Date{Time: period.End, Valid: true}

	expenses, err := s.queries.ListExpensesForVatReturn(ctx, vatdb.ListExpensesForVatReturnParams{OrganisationID: authOrgID, FromDate: from, ToDate: to})
	if err != nil {
		return nil, kernel.ErrInternal(err)
	}
	invoices, err := s.queries.ListInvoicesForVatReturn(ctx, vatdb.ListInvoicesForVatReturnParams{OrganisationID: authOrgID, FromDate: from, ToDate: to})
	if err != nil {
		return nil, kernel.ErrInternal(err)
	}
	bills, err := s.queries.ListBillsForVatReturn(ctx, vatdb.ListBillsForVatReturnParams{OrganisationID: authOrgID, FromDate: from, ToDate: to})
	if err != nil {
		return nil, kernel.ErrInternal(err)
	}
	bankLines, err := s.queries.ListExplanationsForVatReturn(ctx, vatdb.ListExplanationsForVatReturnParams{OrganisationID: authOrgID, FromDate: from, ToDate: to})
	if err != nil {
		return nil, kernel.ErrInternal(err)
	}

	boxes, sales, purchases := computeReturn(expenses, invoices, bills, bankLines)

	basis := "invoice"
	if org.VatAccountingBasis.Valid && org.VatAccountingBasis.String != "" {
		basis = org.VatAccountingBasis.String
	}
	status := "Open"
	if period.End.Before(today) {
		status = "Unfiled"
	}

	resp := &VatReturnResponse{
		PeriodKey:       period.End.Format("2006-01-02"),
		Label:           period.End.Format("01 06"),
		StartDate:       period.Start.Format("2006-01-02"),
		EndDate:         period.End.Format("2006-01-02"),
		DueOn:           period.Due.Format("2006-01-02"),
		DisplayStatus:   status,
		AccountingBasis: basis,
		SalesLines:      linesToResponse(sales),
		PurchaseLines:   linesToResponse(purchases),
	}
	boxesToResponse(boxes, resp)
	return resp, nil
}

// =============================================================================
// VALIDATION / NORMALISATION HELPERS
// =============================================================================

// normaliseVRN trims and reduces the input to the bare 9 digits we store. It is
// forgiving about how a user types it: a leading "GB" prefix and any spaces are
// stripped (so "GB 123 456 789" → "123456789"). nil/blank → NULL; anything that
// is not exactly 9 digits after cleanup is rejected (422).
func normaliseVRN(raw *string) (pgtype.Text, error) {
	if raw == nil {
		return pgtype.Text{Valid: false}, nil
	}
	v := strings.TrimSpace(*raw)
	if v == "" {
		return pgtype.Text{Valid: false}, nil
	}
	v = strings.ReplaceAll(strings.ToUpper(v), " ", "")
	v = strings.TrimPrefix(v, "GB")
	if !vrnDigits.MatchString(v) {
		return pgtype.Text{}, kernel.ErrValidation("vrn must be 9 digits", nil)
	}
	return pgtype.Text{String: v, Valid: true}, nil
}

// normaliseEnum lower-cases + trims a *string and checks it against the allowed
// set. nil/blank → NULL; an out-of-set value is rejected (422).
func normaliseEnum(raw *string, allowed map[string]bool, field, list string) (pgtype.Text, error) {
	if raw == nil {
		return pgtype.Text{Valid: false}, nil
	}
	v := strings.ToLower(strings.TrimSpace(*raw))
	if v == "" {
		return pgtype.Text{Valid: false}, nil
	}
	if !allowed[v] {
		return pgtype.Text{}, kernel.ErrValidation(field+" must be one of "+list, nil)
	}
	return pgtype.Text{String: v, Valid: true}, nil
}

// parseDatePtr parses a YYYY-MM-DD *string into a pgtype.Date. nil/blank → NULL;
// a malformed date is rejected (422).
func parseDatePtr(s *string, field string) (pgtype.Date, error) {
	if s == nil || strings.TrimSpace(*s) == "" {
		return pgtype.Date{Valid: false}, nil
	}
	t, err := time.Parse("2006-01-02", strings.TrimSpace(*s))
	if err != nil {
		return pgtype.Date{}, kernel.ErrValidation(field+" must be in YYYY-MM-DD format", err)
	}
	return pgtype.Date{Time: t, Valid: true}, nil
}

// flatRateToBps converts the flat-rate percentage string to basis points, reusing
// the money kernel (so "10.5" → 1050). Only meaningful when on the scheme; off the
// scheme, or with no percentage given, it stores NULL.
func flatRateToBps(onScheme bool, pct *string) (pgtype.Int4, error) {
	if !onScheme || pct == nil || strings.TrimSpace(*pct) == "" {
		return pgtype.Int4{Valid: false}, nil
	}
	bps, err := money.PercentToBps(strings.TrimSpace(*pct))
	if err != nil {
		return pgtype.Int4{}, kernel.ErrValidation("flat_rate_percentage must be a number, e.g. 10.5", err)
	}
	return pgtype.Int4{Int32: bps, Valid: true}, nil
}

// =============================================================================
// RESPONSE FORMATTER
// =============================================================================

// vatSettingsToResponse projects the VAT columns of an organisation row into the
// API response shape. The flat-rate bps is rendered back to a percentage string
// (money.BpsToPercentString) so the round-trip matches what the form sent.
func vatSettingsToResponse(o auth.Organisation) *VatSettingsResponse {
	return &VatSettingsResponse{
		VatRegistered:        o.VatRegistered,
		Vrn:                  kernel.NullTextToPtr(o.Vrn),
		UsesNonStandardRates: o.VatUsesNonStandardRates,
		EffectiveDate:        dateToStringPtr(o.VatEffectiveDate),
		FirstReturnPeriodEnd: dateToStringPtr(o.VatFirstReturnPeriodEnd),
		ReturnFrequency:      kernel.NullTextToPtr(o.VatReturnFrequency),
		AccountingBasis:      kernel.NullTextToPtr(o.VatAccountingBasis),
		FlatRateScheme:       o.VatFlatRateScheme,
		FlatRatePercentage:   bpsToPercentPtr(o.VatFlatRateBps),
		PreRegExpenseMonths:  int4ToPtr(o.VatPreRegExpenseMonths),
	}
}

// dateToStringPtr renders a nullable DATE as a YYYY-MM-DD *string; nil when NULL.
func dateToStringPtr(d pgtype.Date) *string {
	if !d.Valid {
		return nil
	}
	s := d.Time.Format("2006-01-02")
	return &s
}

// bpsToPercentPtr renders a nullable bps rate as a percentage *string ("1050" →
// "10.5"); nil when NULL.
func bpsToPercentPtr(n pgtype.Int4) *string {
	if !n.Valid {
		return nil
	}
	s := money.BpsToPercentString(n.Int32)
	return &s
}

// int4ToPtr converts a nullable pgtype.Int4 to *int32; nil when NULL.
func int4ToPtr(n pgtype.Int4) *int32 {
	if !n.Valid {
		return nil
	}
	v := n.Int32
	return &v
}
