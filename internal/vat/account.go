package vat

// account.go
// =============================================================================
// VAT dashboard service methods — the read layer over HMRC's MTD VAT account
// (obligations, view-return, liabilities, payments, penalties, financial-details,
// information). Each method: authorise (any active member) → hmrcAccess (VRN +
// token) → call the hmrc.go fetcher → map to a boundary DTO. HMRC is the system of
// record for this data; we never persist it.
//
// Money: HMRC sends amounts as JSON numbers; we format them to fixed-dp strings
// with shopspring/decimal (never float), per the repo's money rule.
// =============================================================================

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"

	"github.com/operationfb/accounting-saas/internal/kernel"
)

// hmrcAccess authorises the caller (any active member), confirms the org has a VRN,
// and returns a fresh HMRC access token + the VAT API base URL. It is the shared
// preamble for every dashboard read: 422 if no VRN, 409 if HMRC isn't connected,
// and GetToken auto-refreshes a near-expiry token.
func (s *Service) hmrcAccess(ctx context.Context, authUserID, authOrgID uuid.UUID) (vrn, accessToken, apiBaseURL string, err error) {
	if _, err = s.authorize(ctx, authUserID, authOrgID); err != nil {
		return "", "", "", err
	}
	org, err := s.loadOrg(ctx, authOrgID)
	if err != nil {
		return "", "", "", err
	}
	if !org.VatRegistered || !org.Vrn.Valid || org.Vrn.String == "" {
		return "", "", "", kernel.ErrValidation("a VAT registration number (VRN) must be set before viewing HMRC data", nil)
	}
	if s.hmrc == nil {
		return "", "", "", kernel.ErrConflict("HMRC connection is not configured")
	}
	accessToken, apiBaseURL, err = s.hmrc.GetToken(ctx, authOrgID)
	if err != nil {
		return "", "", "", err
	}
	return org.Vrn.String, accessToken, apiBaseURL, nil
}

// --- money / format helpers ---

// hmrcDecimal parses an HMRC JSON amount into a decimal (zero on blank/garbage), so
// money never round-trips through float64.
func hmrcDecimal(n json.Number) decimal.Decimal {
	if n == "" {
		return decimal.Zero
	}
	d, err := decimal.NewFromString(n.String())
	if err != nil {
		return decimal.Zero
	}
	return d
}

// hmrcMoney renders an HMRC amount as a 2-dp pound string (e.g. 1000 → "1000.00").
func hmrcMoney(n json.Number) string { return hmrcDecimal(n).StringFixed(2) }

// hmrcWholePounds renders boxes 6–9 as whole-pound strings (HMRC's own convention).
func hmrcWholePounds(n json.Number) string { return hmrcDecimal(n).StringFixed(0) }

// emptyToNil maps a blank string to nil (for omitempty pointer fields).
func emptyToNil(s string) *string {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	return &s
}

// hmrcWindow resolves the [from,to] query window for the range-based reads. With
// both blank it defaults to a SAFE ~360-day window ending aheadDays past today (to
// catch the current open obligation); with both given it validates YYYY-MM-DD,
// ordering, and the span. One-sided input is rejected (422).
//
// Why 360 and not 365/366: HMRC's liabilities/payments endpoints nominally allow up
// to 366 days, but reject ranges right at that boundary — a one-year span such as
// 2025-06-25→2026-06-25 (365 days) comes back DATE_RANGE_INVALID (an HMRC off-by-one
// / leap-year quirk; see hmrc/vat-api#556). 360 days stays clear of it, matching the
// proven obligationsWindow in reconcile.go.
func hmrcWindow(fromQ, toQ string, today time.Time, aheadDays int) (from, to string, err error) {
	const safeWindowDays = 360
	fromQ, toQ = strings.TrimSpace(fromQ), strings.TrimSpace(toQ)
	if fromQ == "" && toQ == "" {
		end := today.AddDate(0, 0, aheadDays)
		return end.AddDate(0, 0, -safeWindowDays).Format("2006-01-02"), end.Format("2006-01-02"), nil
	}
	if fromQ == "" || toQ == "" {
		return "", "", kernel.ErrValidation("both from and to are required (YYYY-MM-DD)", nil)
	}
	ft, err := time.Parse("2006-01-02", fromQ)
	if err != nil {
		return "", "", kernel.ErrValidation("from must be in YYYY-MM-DD format", err)
	}
	tt, err := time.Parse("2006-01-02", toQ)
	if err != nil {
		return "", "", kernel.ErrValidation("to must be in YYYY-MM-DD format", err)
	}
	if tt.Before(ft) {
		return "", "", kernel.ErrValidation("to must be on or after from", nil)
	}
	if tt.Sub(ft) > 366*24*time.Hour {
		return "", "", kernel.ErrValidation("from and to must be at most 366 days apart", nil)
	}
	return fromQ, toQ, nil
}

// =============================================================================
// SERVICE METHODS
// =============================================================================

// GetHMRCObligations lists the org's VAT obligations (return periods) from HMRC.
// Optional status "O"/"F" filter (applied in Go — the shared listHMRCObligations
// fetches all). Default window is a trailing ~year reaching ~a month ahead, so the
// current open obligation is included.
func (s *Service) GetHMRCObligations(ctx context.Context, authUserID, authOrgID uuid.UUID, fromQ, toQ, statusQ string) ([]HMRCObligationResponse, error) {
	vrn, token, base, err := s.hmrcAccess(ctx, authUserID, authOrgID)
	if err != nil {
		return nil, err
	}
	status := strings.ToUpper(strings.TrimSpace(statusQ))
	if status != "" && status != "O" && status != "F" {
		return nil, kernel.ErrValidation("status must be O (open) or F (fulfilled)", nil)
	}
	from, to, err := hmrcWindow(fromQ, toQ, dateOnlyUTC(time.Now().UTC()), 35)
	if err != nil {
		return nil, err
	}
	obs, err := listHMRCObligations(ctx, s.httpClient, base, vrn, token, from, to)
	if err != nil {
		return nil, err
	}
	out := make([]HMRCObligationResponse, 0, len(obs))
	for _, o := range obs {
		if status != "" && o.Status != status {
			continue
		}
		out = append(out, HMRCObligationResponse{
			PeriodKey: o.PeriodKey,
			Start:     o.Start,
			End:       o.End,
			Due:       o.Due,
			Status:    o.Status,
			Received:  emptyToNil(o.Received),
		})
	}
	return out, nil
}

// GetHMRCReturn returns HMRC's view of a submitted return (the 9 boxes) for a
// periodKey. 404 if HMRC has no return for that key.
func (s *Service) GetHMRCReturn(ctx context.Context, authUserID, authOrgID uuid.UUID, periodKey string) (*HMRCReturnResponse, error) {
	vrn, token, base, err := s.hmrcAccess(ctx, authUserID, authOrgID)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(periodKey) == "" {
		return nil, kernel.ErrValidation("periodKey is required", nil)
	}
	r, err := fetchHMRCReturn(ctx, s.httpClient, base, vrn, token, periodKey)
	if err != nil {
		return nil, err
	}
	return &HMRCReturnResponse{
		PeriodKey: r.PeriodKey,
		Box1:      hmrcMoney(r.VatDueSales),
		Box2:      hmrcMoney(r.VatDueAcquisitions),
		Box3:      hmrcMoney(r.TotalVatDue),
		Box4:      hmrcMoney(r.VatReclaimedCurrPeriod),
		Box5:      hmrcMoney(r.NetVatDue),
		Box6:      hmrcWholePounds(r.TotalValueSalesExVAT),
		Box7:      hmrcWholePounds(r.TotalValuePurchasesExVAT),
		Box8:      hmrcWholePounds(r.TotalValueGoodsSuppliedExVAT),
		Box9:      hmrcWholePounds(r.TotalAcquisitionsExVAT),
	}, nil
}

// GetHMRCLiabilities lists amounts owed to HMRC over the window (default ~year).
func (s *Service) GetHMRCLiabilities(ctx context.Context, authUserID, authOrgID uuid.UUID, fromQ, toQ string) ([]HMRCLiabilityResponse, error) {
	vrn, token, base, err := s.hmrcAccess(ctx, authUserID, authOrgID)
	if err != nil {
		return nil, err
	}
	from, to, err := hmrcWindow(fromQ, toQ, dateOnlyUTC(time.Now().UTC()), 0)
	if err != nil {
		return nil, err
	}
	ls, err := fetchHMRCLiabilities(ctx, s.httpClient, base, vrn, token, from, to)
	if err != nil {
		return nil, err
	}
	out := make([]HMRCLiabilityResponse, 0, len(ls))
	for _, l := range ls {
		resp := HMRCLiabilityResponse{
			Type:              l.Type,
			OriginalAmount:    hmrcMoney(l.OriginalAmount),
			OutstandingAmount: hmrcMoney(l.OutstandingAmount),
			Due:               emptyToNil(l.Due),
		}
		if l.TaxPeriod != nil {
			resp.From = emptyToNil(l.TaxPeriod.From)
			resp.To = emptyToNil(l.TaxPeriod.To)
		}
		out = append(out, resp)
	}
	return out, nil
}

// GetHMRCPayments lists payments received by HMRC over the window (default ~year).
func (s *Service) GetHMRCPayments(ctx context.Context, authUserID, authOrgID uuid.UUID, fromQ, toQ string) ([]HMRCPaymentResponse, error) {
	vrn, token, base, err := s.hmrcAccess(ctx, authUserID, authOrgID)
	if err != nil {
		return nil, err
	}
	from, to, err := hmrcWindow(fromQ, toQ, dateOnlyUTC(time.Now().UTC()), 0)
	if err != nil {
		return nil, err
	}
	ps, err := fetchHMRCPayments(ctx, s.httpClient, base, vrn, token, from, to)
	if err != nil {
		return nil, err
	}
	out := make([]HMRCPaymentResponse, 0, len(ps))
	for _, p := range ps {
		out = append(out, HMRCPaymentResponse{
			Amount:   hmrcMoney(p.Amount),
			Received: emptyToNil(p.Received),
		})
	}
	return out, nil
}

// GetHMRCPenalties returns the late-submission points summary + the penalty charges
// (each with the charge reference that drills into financial-details).
func (s *Service) GetHMRCPenalties(ctx context.Context, authUserID, authOrgID uuid.UUID) (*HMRCPenaltiesResponse, error) {
	vrn, token, base, err := s.hmrcAccess(ctx, authUserID, authOrgID)
	if err != nil {
		return nil, err
	}
	p, err := fetchHMRCPenalties(ctx, s.httpClient, base, vrn, token)
	if err != nil {
		return nil, err
	}
	out := &HMRCPenaltiesResponse{Penalties: []HMRCPenaltyChargeResponse{}, TotalPenalties: "0.00"}
	if p.LateSubmissionPenalty != nil {
		if sm := p.LateSubmissionPenalty.Summary; sm != nil {
			out.ActivePoints = sm.ActivePenaltyPoints
			out.InactivePoints = sm.InactivePenaltyPoints
			out.Threshold = sm.RegimeThreshold
		}
		for _, d := range p.LateSubmissionPenalty.Details {
			out.Penalties = append(out.Penalties, HMRCPenaltyChargeResponse{
				Type:            "late_submission",
				Category:        d.PenaltyCategory,
				ChargeReference: d.PenaltyChargeReference,
				Status:          d.PenaltyStatus,
				Amount:          hmrcMoney(d.ChargeAmount),
			})
		}
	}
	if p.LatePaymentPenalty != nil {
		for _, d := range p.LatePaymentPenalty.Details {
			out.Penalties = append(out.Penalties, HMRCPenaltyChargeResponse{
				Type:            "late_payment",
				Category:        d.PenaltyCategory,
				ChargeReference: d.PenaltyChargeReference,
				Status:          d.PenaltyStatus,
				Amount:          hmrcMoney(d.PenaltyAmountOutstanding),
			})
		}
	}
	if p.Totalisations != nil {
		out.TotalPenalties = hmrcDecimal(p.Totalisations.LSPTotalValue).
			Add(hmrcDecimal(p.Totalisations.LPPPostedTotal)).StringFixed(2)
	}
	return out, nil
}

// GetHMRCFinancialDetails returns the charge breakdown for one penalty charge ref.
func (s *Service) GetHMRCFinancialDetails(ctx context.Context, authUserID, authOrgID uuid.UUID, chargeRef string) (*HMRCFinancialDetailsResponse, error) {
	vrn, token, base, err := s.hmrcAccess(ctx, authUserID, authOrgID)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(chargeRef) == "" {
		return nil, kernel.ErrValidation("a penalty charge reference is required", nil)
	}
	fd, err := fetchHMRCFinancialDetails(ctx, s.httpClient, base, vrn, token, chargeRef)
	if err != nil {
		return nil, err
	}
	out := &HMRCFinancialDetailsResponse{ChargeReference: chargeRef, Documents: []HMRCFinancialDocResponse{}}
	for _, d := range fd.DocumentDetails {
		out.Documents = append(out.Documents, HMRCFinancialDocResponse{
			Type:              d.DocumentType,
			ChargeReference:   d.ChargeReferenceNumber,
			TotalAmount:       hmrcMoney(d.DocumentTotalAmount),
			OutstandingAmount: hmrcMoney(d.DocumentOutstandingAmount),
			DueDate:           emptyToNil(d.DocumentDueDate),
		})
	}
	return out, nil
}

// GetHMRCInformation returns the registered VAT business details.
func (s *Service) GetHMRCInformation(ctx context.Context, authUserID, authOrgID uuid.UUID) (*HMRCInformationResponse, error) {
	vrn, token, base, err := s.hmrcAccess(ctx, authUserID, authOrgID)
	if err != nil {
		return nil, err
	}
	inf, err := fetchHMRCInformation(ctx, s.httpClient, base, vrn, token)
	if err != nil {
		return nil, err
	}
	out := &HMRCInformationResponse{
		BusinessName:     inf.OrganisationName,
		TradingName:      inf.TradingName,
		RegistrationDate: emptyToNil(inf.RegistrationDate),
	}
	if out.BusinessName == "" && inf.IndividualName != nil {
		out.BusinessName = strings.TrimSpace(inf.IndividualName.FirstName + " " + inf.IndividualName.LastName)
	}
	if a := inf.BusinessAddress; a != nil {
		for _, ln := range []string{a.Line1, a.Line2, a.Line3, a.Line4} {
			if strings.TrimSpace(ln) != "" {
				out.AddressLines = append(out.AddressLines, ln)
			}
		}
		out.Postcode = a.Postcode
		out.CountryCode = a.CountryCode
	}
	return out, nil
}
