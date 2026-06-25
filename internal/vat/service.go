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
	"net/http"
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

// HMRCConnector is the narrow cross-domain seam from the VAT service to the
// HMRC integration. Defined here (not in internal/integrations) to avoid a
// domain import cycle. *integrations.Service satisfies this interface via its
// IsConnected and GetToken methods added in internal/integrations/service.go.
type HMRCConnector interface {
	// IsConnected reports whether this org has an active HMRC OAuth connection.
	IsConnected(ctx context.Context, orgID uuid.UUID) (bool, *time.Time)
	// GetToken returns a valid access token and the HMRC VAT API base URL.
	// Auto-refreshes if the token is near expiry. Returns 409 if not connected.
	GetToken(ctx context.Context, orgID uuid.UUID) (accessToken, apiBaseURL string, err error)
}

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
	// hmrc is the cross-domain seam to the HMRC integration service — used to
	// check connection status (in GetSettings) and obtain access tokens (in
	// SubmitReturn). nil when HMRC is not wired (graceful: hmrc_connected = false).
	hmrc       HMRCConnector
	httpClient *http.Client
}

// NewService is the constructor, called once in main.go. authQueries is the shared
// auth.Querier; queries is the VAT read-query set (db/vat) used by GetReturn.
// hmrc may be nil — it disables the HMRC connection check and submission.
func NewService(authQueries auth.Querier, queries *vatdb.Queries, hmrc HMRCConnector) *Service {
	return &Service{
		authQueries: authQueries,
		queries:     queries,
		hmrc:        hmrc,
		httpClient:  &http.Client{Timeout: 30 * time.Second},
	}
}

// SetHMRC wires the HMRC integration seam after construction. Used by main.go
// because vatSvc is built before hmrcIntegrationSvc in the dependency graph
// (same pattern as expenses.SetPublisher for the Pub/Sub publisher).
func (s *Service) SetHMRC(connector HMRCConnector) { s.hmrc = connector }

// authorize confirms the caller is an ACTIVE member and returns their role
// (the role lets UpdateSettings gate editing to owners/admins).
func (s *Service) authorize(ctx context.Context, userID, orgID uuid.UUID) (auth.OrganisationRole, error) {
	return kernel.AuthorizeMember(ctx, s.authQueries, userID, orgID)
}

// =============================================================================
// GET
// =============================================================================

// GetSettings returns the caller's organisation's VAT settings. Any active
// member may read; the org is taken from the token. The response includes
// hmrc_connected — derived live from the integrations table — so the SPA can
// enable the "Submit to HMRC" button without a separate API call.
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
	resp := vatSettingsToResponse(org)

	// Merge the HMRC connection status. The check is a light DB read that never
	// fails the whole request — if the seam is absent (nil) or the DB errors, we
	// just leave hmrc_connected=false.
	if s.hmrc != nil {
		connected, connectedAt := s.hmrc.IsConnected(ctx, authOrgID)
		resp.HMRCConnected = connected
		if connectedAt != nil {
			ts := connectedAt.Format(time.RFC3339)
			resp.HMRCConnectedAt = &ts
		}
	}
	return resp, nil
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

	// Overlay the saved filing status onto each period (a filed period shows
	// "Marked as filed" instead of "Unfiled"), keyed by the period-end date.
	summaries, err := s.queries.ListVatReturnSummaries(ctx, authOrgID)
	if err != nil {
		return nil, kernel.ErrInternal(err)
	}
	filed := make(map[string]string, len(summaries))
	for _, sm := range summaries {
		if sm.PeriodEnd.Valid {
			filed[sm.PeriodEnd.Time.Format("2006-01-02")] = sm.FilingStatus
		}
	}

	// Map to DTOs, reversing to newest-first.
	out := make([]VatPeriodResponse, 0, len(periods))
	for i := len(periods) - 1; i >= 0; i-- {
		p := periods[i]
		ended := p.End.Before(today)
		key := p.End.Format("2006-01-02")
		out = append(out, VatPeriodResponse{
			PeriodKey:     key,
			Label:         p.End.Format("01 06"), // "MM YY", e.g. "05 26"
			StartDate:     p.Start.Format("2006-01-02"),
			EndDate:       key,
			DueOn:         p.Due.Format("2006-01-02"),
			Ended:         ended,
			DisplayStatus: displayStatusFor(filed[key], ended),
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
// The org's vat_accounting_basis selects the computation:
//   - invoice/accrual: the documents (invoices SENT, bills, expenses APPROVED/PAID)
//     by date + direct-category bank explanations.
//   - cash: invoices/bills via the bank transactions that settle them (receipts /
//     payments, apportioned), + expenses by date + the same direct-category entries.
func (s *Service) GetReturn(ctx context.Context, authUserID, authOrgID uuid.UUID, periodKey string) (*VatReturnResponse, error) {
	if _, err := s.authorize(ctx, authUserID, authOrgID); err != nil {
		return nil, err
	}
	org, err := s.loadOrg(ctx, authOrgID)
	if err != nil {
		return nil, err
	}
	period, err := s.resolvePeriod(org, periodKey)
	if err != nil {
		return nil, err
	}

	today := dateOnlyUTC(time.Now().UTC())

	// A saved return overlays the display status; a FILED one also takes over the FIGURES.
	saved, err := s.savedReturn(ctx, authOrgID, period)
	if err != nil {
		return nil, err
	}

	// FILED period → render from the frozen snapshot, NOT a live recompute, so the boxes
	// shown are exactly what was filed and can't drift (even if the lock is ever bypassed).
	// The basis is the one used at filing time; the Full-Report lines are recomputed on
	// THAT basis — the period is locked, so they reproduce the filed breakdown — while the
	// boxes come straight from the stored snapshot.
	if saved != nil && isFiledStatus(saved.FilingStatus) {
		_, sales, purchases, err := s.computeBoxes(ctx, authOrgID, saved.AccountingBasis, period)
		if err != nil {
			return nil, err
		}
		resp := buildReturnResponse(period, saved.AccountingBasis, today, savedBoxes(saved), sales, purchases)
		resp.DisplayStatus = displayStatusFor(saved.FilingStatus, period.End.Before(today))
		return resp, nil
	}

	// UNFILED (or rejected) → compute live on the org's CURRENT basis, overlaying any
	// saved display status (e.g. a Phase-2 "Rejected").
	basis := accountingBasis(org)
	boxes, sales, purchases, err := s.computeBoxes(ctx, authOrgID, basis, period)
	if err != nil {
		return nil, err
	}
	resp := buildReturnResponse(period, basis, today, boxes, sales, purchases)
	if saved != nil {
		resp.DisplayStatus = displayStatusFor(saved.FilingStatus, period.End.Before(today))
	}
	return resp, nil
}

// =============================================================================
// MARK AS FILED — persists the snapshot + LOCKS the period.
// =============================================================================

// MarkFiled snapshots the computed return for a period into vat_returns and sets its
// filing_status to marked_as_filed. That makes the period a FILED PERIOD: the source
// domains then refuse to edit or delete any record dated inside it (the lock). Owner/
// admin only. Returns the now-filed return.
func (s *Service) MarkFiled(ctx context.Context, authUserID, authOrgID uuid.UUID, periodKey string) (*VatReturnResponse, error) {
	role, err := s.authorize(ctx, authUserID, authOrgID)
	if err != nil {
		return nil, err
	}
	if !kernel.IsOrgAdmin(role) {
		return nil, kernel.ErrForbidden("only owners and admins can file a VAT return")
	}
	org, err := s.loadOrg(ctx, authOrgID)
	if err != nil {
		return nil, err
	}
	period, err := s.resolvePeriod(org, periodKey)
	if err != nil {
		return nil, err
	}
	basis := accountingBasis(org)
	boxes, sales, purchases, err := s.computeBoxes(ctx, authOrgID, basis, period)
	if err != nil {
		return nil, err
	}

	// A payment is owed to HMRC only when the net VAT (Box 5) is positive; a refund or
	// nil return has no payment status.
	var paymentStatus pgtype.Text
	if boxes.Box5 > 0 {
		paymentStatus = pgtype.Text{String: "unpaid", Valid: true}
	}

	if err := s.queries.UpsertVatReturnFiled(ctx, vatdb.UpsertVatReturnFiledParams{
		OrganisationID:        authOrgID,
		CreatedByUserID:       pgtype.UUID{Bytes: authUserID, Valid: true},
		PeriodStart:           pgtype.Date{Time: period.Start, Valid: true},
		PeriodEnd:             pgtype.Date{Time: period.End, Valid: true},
		PeriodKey:             period.End.Format("2006-01-02"),
		AccountingBasis:       basis,
		Box1:                  boxes.Box1,
		Box2:                  boxes.Box2,
		Box3:                  boxes.Box3,
		Box4:                  boxes.Box4,
		Box5:                  boxes.Box5,
		Box6:                  boxes.Box6,
		Box7:                  boxes.Box7,
		Box8:                  boxes.Box8,
		Box9:                  boxes.Box9,
		FilingDueOn:           pgtype.Date{Time: period.Due, Valid: true},
		PaymentDueOn:          pgtype.Date{Time: period.Due, Valid: true},
		PaymentAmountDueMinor: pgtype.Int8{Int64: boxes.Box5, Valid: true},
		PaymentStatus:         paymentStatus,
	}); err != nil {
		return nil, kernel.ErrInternal(err)
	}

	resp := buildReturnResponse(period, basis, dateOnlyUTC(time.Now().UTC()), boxes, sales, purchases)
	resp.DisplayStatus = "Marked as filed"
	return resp, nil
}

// =============================================================================
// SUBMIT TO HMRC — online MTD submission.
// =============================================================================

// SubmitReturn submits the computed VAT return for a period to HMRC via the
// Making Tax Digital API, then stores the HMRC response in vat_returns with
// filing_status = 'filed'. Owner/admin only.
//
// Flow:
//  1. Validate org has a VRN and is VAT-registered.
//  2. Get HMRC access token (409 if not connected).
//  3. Fetch HMRC obligations for the VRN, find one matching our period.
//  4. Compute the 9 boxes from live data.
//  5. POST the return to HMRC.
//  6. On success: upsert vat_returns with the HMRC response and return
//     the form bundle number + processing date to the caller.
func (s *Service) SubmitReturn(ctx context.Context, authUserID, authOrgID uuid.UUID, periodKey string) (*VatSubmitResponse, error) {
	role, err := s.authorize(ctx, authUserID, authOrgID)
	if err != nil {
		return nil, err
	}
	if !kernel.IsOrgAdmin(role) {
		return nil, kernel.ErrForbidden("only owners and admins can submit a VAT return to HMRC")
	}

	org, err := s.loadOrg(ctx, authOrgID)
	if err != nil {
		return nil, err
	}
	if !org.VatRegistered || !org.Vrn.Valid || org.Vrn.String == "" {
		return nil, kernel.ErrValidation("a VAT registration number (VRN) must be set before submitting to HMRC", nil)
	}
	vrn := org.Vrn.String

	// Get HMRC access token — 409 if not connected; auto-refreshes if near expiry.
	if s.hmrc == nil {
		return nil, kernel.ErrConflict("HMRC connection is not configured")
	}
	accessToken, apiBaseURL, err := s.hmrc.GetToken(ctx, authOrgID)
	if err != nil {
		return nil, err
	}

	period, err := s.resolvePeriod(org, periodKey)
	if err != nil {
		return nil, err
	}
	if !period.End.Before(dateOnlyUTC(time.Now().UTC())) {
		return nil, kernel.ErrValidation("the period has not ended yet and cannot be submitted", nil)
	}

	// Find the HMRC obligation for this period — provides the HMRC periodKey
	// (e.g. "18A1") required in the submission body.
	obligation, err := fetchHMRCObligation(ctx, s.httpClient, apiBaseURL, vrn, accessToken, periodKey)
	if err != nil {
		return nil, err
	}

	basis := accountingBasis(org)
	boxes, _, _, err := s.computeBoxes(ctx, authOrgID, basis, period)
	if err != nil {
		return nil, err
	}

	// POST to HMRC. hmrc.go converts pence boxes to the pound/whole-pound values
	// the API expects.
	hmrcResp, err := postHMRCReturn(ctx, s.httpClient, apiBaseURL, vrn, accessToken, obligation.PeriodKey, boxes)
	if err != nil {
		return nil, err
	}

	// Parse HMRC's processingDate (ISO8601) to a timestamptz for DB storage.
	processingTime, err := time.Parse(time.RFC3339, hmrcResp.ProcessingDate)
	if err != nil {
		// Some HMRC sandbox responses use millisecond precision.
		processingTime, err = time.Parse("2006-01-02T15:04:05.999Z07:00", hmrcResp.ProcessingDate)
		if err != nil {
			// If we still can't parse it, fall back to now so the DB write doesn't fail.
			processingTime = time.Now().UTC()
		}
	}

	var paymentStatus pgtype.Text
	if boxes.Box5 > 0 {
		paymentStatus = pgtype.Text{String: "unpaid", Valid: true}
	}
	chargeRef := pgtype.Text{String: hmrcResp.ChargeRefNumber, Valid: hmrcResp.ChargeRefNumber != ""}
	bundleNum := pgtype.Text{String: hmrcResp.FormBundleNumber, Valid: hmrcResp.FormBundleNumber != ""}

	if err := s.queries.UpsertVatReturnHmrcFiled(ctx, vatdb.UpsertVatReturnHmrcFiledParams{
		OrganisationID:        authOrgID,
		CreatedByUserID:       pgtype.UUID{Bytes: authUserID, Valid: true},
		PeriodStart:           pgtype.Date{Time: period.Start, Valid: true},
		PeriodEnd:             pgtype.Date{Time: period.End, Valid: true},
		PeriodKey:             periodKey,
		AccountingBasis:       basis,
		Box1:                  boxes.Box1,
		Box2:                  boxes.Box2,
		Box3:                  boxes.Box3,
		Box4:                  boxes.Box4,
		Box5:                  boxes.Box5,
		Box6:                  boxes.Box6,
		Box7:                  boxes.Box7,
		Box8:                  boxes.Box8,
		Box9:                  boxes.Box9,
		FilingDueOn:           pgtype.Date{Time: period.Due, Valid: true},
		ProcessingDate:        pgtype.Timestamptz{Time: processingTime, Valid: true},
		FormBundleNumber:      bundleNum,
		PaymentDueOn:          pgtype.Date{Time: period.Due, Valid: true},
		PaymentAmountDueMinor: pgtype.Int8{Int64: boxes.Box5, Valid: true},
		PaymentStatus:         paymentStatus,
		ChargeRefNumber:       chargeRef,
	}); err != nil {
		return nil, kernel.ErrInternal(err)
	}

	resp := &VatSubmitResponse{
		PeriodKey:        periodKey,
		FormBundleNumber: hmrcResp.FormBundleNumber,
		ProcessingDate:   hmrcResp.ProcessingDate,
	}
	if hmrcResp.ChargeRefNumber != "" {
		s := hmrcResp.ChargeRefNumber
		resp.ChargeRefNumber = &s
	}
	return resp, nil
}

// =============================================================================
// RETURN HELPERS (shared by GetReturn + MarkFiled)
// =============================================================================

// loadOrg fetches the caller's organisation (404 if soft-deleted).
func (s *Service) loadOrg(ctx context.Context, orgID uuid.UUID) (auth.Organisation, error) {
	org, err := s.authQueries.GetOrganisation(ctx, orgID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return auth.Organisation{}, kernel.ErrNotFound("organisation", orgID.String())
		}
		return auth.Organisation{}, kernel.ErrInternal(err)
	}
	return org, nil
}

// accountingBasis returns the org's configured basis, defaulting to invoice.
func accountingBasis(org auth.Organisation) string {
	if org.VatAccountingBasis.Valid && org.VatAccountingBasis.String != "" {
		return org.VatAccountingBasis.String
	}
	return "invoice"
}

// resolvePeriod finds the period whose end == periodKey in the org's generated
// schedule. 404 when the org isn't configured or the key isn't a real period.
func (s *Service) resolvePeriod(org auth.Organisation, periodKey string) (*vatPeriod, error) {
	if !org.VatRegistered || !org.VatEffectiveDate.Valid ||
		!org.VatFirstReturnPeriodEnd.Valid || !org.VatReturnFrequency.Valid {
		return nil, kernel.ErrNotFound("vat return period", periodKey)
	}
	periods := generateVATPeriods(
		org.VatEffectiveDate.Time,
		org.VatFirstReturnPeriodEnd.Time,
		dateOnlyUTC(time.Now().UTC()),
		org.VatReturnFrequency.String,
	)
	for i := range periods {
		if periods[i].End.Format("2006-01-02") == periodKey {
			return &periods[i], nil
		}
	}
	return nil, kernel.ErrNotFound("vat return period", periodKey)
}

// computeBoxes fetches the period's source rows for the basis and runs the engine.
func (s *Service) computeBoxes(ctx context.Context, orgID uuid.UUID, basis string, p *vatPeriod) (vatBoxes, []vatLine, []vatLine, error) {
	from := pgtype.Date{Time: p.Start, Valid: true}
	to := pgtype.Date{Time: p.End, Valid: true}

	// Both bases share expenses (by date) + direct-category bank explanations.
	expenses, err := s.queries.ListExpensesForVatReturn(ctx, vatdb.ListExpensesForVatReturnParams{OrganisationID: orgID, FromDate: from, ToDate: to})
	if err != nil {
		return vatBoxes{}, nil, nil, kernel.ErrInternal(err)
	}
	bankLines, err := s.queries.ListExplanationsForVatReturn(ctx, vatdb.ListExplanationsForVatReturnParams{OrganisationID: orgID, FromDate: from, ToDate: to})
	if err != nil {
		return vatBoxes{}, nil, nil, kernel.ErrInternal(err)
	}

	if basis == "cash" {
		receipts, err := s.queries.ListInvoiceReceiptsForVatReturn(ctx, vatdb.ListInvoiceReceiptsForVatReturnParams{OrganisationID: orgID, FromDate: from, ToDate: to})
		if err != nil {
			return vatBoxes{}, nil, nil, kernel.ErrInternal(err)
		}
		payments, err := s.queries.ListBillPaymentsForVatReturn(ctx, vatdb.ListBillPaymentsForVatReturnParams{OrganisationID: orgID, FromDate: from, ToDate: to})
		if err != nil {
			return vatBoxes{}, nil, nil, kernel.ErrInternal(err)
		}
		b, sales, purchases := computeReturnCash(expenses, receipts, payments, bankLines)
		return b, sales, purchases, nil
	}

	invoices, err := s.queries.ListInvoicesForVatReturn(ctx, vatdb.ListInvoicesForVatReturnParams{OrganisationID: orgID, FromDate: from, ToDate: to})
	if err != nil {
		return vatBoxes{}, nil, nil, kernel.ErrInternal(err)
	}
	bills, err := s.queries.ListBillsForVatReturn(ctx, vatdb.ListBillsForVatReturnParams{OrganisationID: orgID, FromDate: from, ToDate: to})
	if err != nil {
		return vatBoxes{}, nil, nil, kernel.ErrInternal(err)
	}
	b, sales, purchases := computeReturnAccrual(expenses, invoices, bills, bankLines)
	return b, sales, purchases, nil
}

// savedReturn fetches the saved vat_returns row for the period, or nil if none has
// been filed/saved (a never-filed period computes live).
func (s *Service) savedReturn(ctx context.Context, orgID uuid.UUID, p *vatPeriod) (*vatdb.GetVatReturnByPeriodRow, error) {
	saved, err := s.queries.GetVatReturnByPeriod(ctx, vatdb.GetVatReturnByPeriodParams{
		OrganisationID: orgID,
		PeriodStart:    pgtype.Date{Time: p.Start, Valid: true},
		PeriodEnd:      pgtype.Date{Time: p.End, Valid: true},
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, kernel.ErrInternal(err)
	}
	return &saved, nil
}

// isFiledStatus reports whether a saved filing_status means the return has been
// SUBMITTED — marked as filed, or (Phase 2) pending/filed with HMRC. For these the
// stored snapshot is authoritative (shown instead of a live recompute) and the period
// is locked against edits. It mirrors EXACTLY the predicate IsDateInFiledPeriod uses,
// so "rendered from snapshot" and "locked" are the same set of states. unfiled and
// rejected fall through to a live recompute (a rejection is fixed by re-deriving).
func isFiledStatus(status string) bool {
	switch status {
	case "marked_as_filed", "filed", "pending":
		return true
	}
	return false
}

// savedBoxes lifts the stored 9-box snapshot into the engine's vatBoxes shape so it
// renders through the same boxesToResponse path as a live computation — boxes 6–9 were
// stored RAW (unrounded) pence and are rounded to whole pounds at render time, exactly
// as they were when the return was filed.
func savedBoxes(r *vatdb.GetVatReturnByPeriodRow) vatBoxes {
	return vatBoxes{
		Box1: r.Box1, Box2: r.Box2, Box3: r.Box3, Box4: r.Box4, Box5: r.Box5,
		Box6: r.Box6, Box7: r.Box7, Box8: r.Box8, Box9: r.Box9,
	}
}

// buildReturnResponse assembles the DTO with a default Open/Unfiled status.
func buildReturnResponse(p *vatPeriod, basis string, today time.Time, boxes vatBoxes, sales, purchases []vatLine) *VatReturnResponse {
	resp := &VatReturnResponse{
		PeriodKey:       p.End.Format("2006-01-02"),
		Label:           p.End.Format("01 06"),
		StartDate:       p.Start.Format("2006-01-02"),
		EndDate:         p.End.Format("2006-01-02"),
		DueOn:           p.Due.Format("2006-01-02"),
		DisplayStatus:   displayStatusFor("", p.End.Before(today)),
		AccountingBasis: basis,
		SalesLines:      linesToResponse(sales),
		PurchaseLines:   linesToResponse(purchases),
	}
	boxesToResponse(boxes, resp)
	return resp
}

// displayStatusFor maps a saved filing_status (or "" for none) + whether the period
// has ended into the user-facing display label.
func displayStatusFor(filingStatus string, ended bool) string {
	switch filingStatus {
	case "marked_as_filed":
		return "Marked as filed"
	case "filed":
		return "Filed"
	case "pending":
		return "Pending"
	case "rejected":
		return "Rejected"
	}
	if ended {
		return "Unfiled"
	}
	return "Open"
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
