package vat

// reconcile.go
// =============================================================================
// HMRC period reconciliation. Our VAT period list/resolution is GENERATED from
// the org's settings (effective_date, first_return_period_end, return_frequency),
// while HMRC keeps the authoritative obligations. The two can drift (most often a
// wrong "stagger"), which only surfaces as a 409 at submit time.
//
// At connect, the SPA calls CheckHMRCPeriods to compare the two; if they differ
// it offers to rewrite the settings to match HMRC (SyncHMRCPeriods). The pure
// derivation (inferFrequency / deriveSettingsFromObligations) is split out so it
// unit-tests without a DB or HTTP, like the period generator it feeds.
//
// Derivation: anchor the generated schedule to HMRC's EARLIEST visible obligation
// — effective_date = its start, first_return_period_end = its end, frequency =
// inferred from its span. Because generateVATPeriods encodes the whole stagger in
// first_return_period_end, that makes the forward schedule line up with HMRC.
// =============================================================================

import (
	"context"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	auth "github.com/operationfb/accounting-saas/db/auth"
	"github.com/operationfb/accounting-saas/internal/kernel"
)

// inferFrequency derives the VAT return frequency from a single obligation's span
// — the number of whole calendar months between its start and the day AFTER its
// end (VAT periods always end on a month-end, so this is exact). Returns "" when
// the span isn't a recognised frequency.
func inferFrequency(start, end time.Time) string {
	next := dateOnlyUTC(end).AddDate(0, 0, 1)
	start = dateOnlyUTC(start)
	months := (next.Year()-start.Year())*12 + int(next.Month()) - int(start.Month())
	switch months {
	case 1:
		return "monthly"
	case 3:
		return "quarterly"
	case 12:
		return "annually"
	default:
		return ""
	}
}

// suggestedSettings is the derived (effective, firstEnd, frequency) triple that
// would align our generated schedule with HMRC's obligations.
type suggestedSettings struct {
	EffectiveDate        time.Time
	FirstReturnPeriodEnd time.Time
	ReturnFrequency      string
}

// deriveSettingsFromObligations anchors the schedule to the EARLIEST obligation
// (by start date). Returns ok=false when the list is empty or the frequency can't
// be inferred — the caller then treats reconciliation as not applicable.
func deriveSettingsFromObligations(obligations []hmrcObligation) (suggestedSettings, bool) {
	var (
		bestEnd   time.Time
		bestStart time.Time
		found     bool
	)
	for i := range obligations {
		s, err := time.Parse("2006-01-02", obligations[i].Start)
		if err != nil {
			continue
		}
		e, err := time.Parse("2006-01-02", obligations[i].End)
		if err != nil {
			continue
		}
		if !found || s.Before(bestStart) {
			bestStart, bestEnd, found = s, e, true
		}
	}
	if !found {
		return suggestedSettings{}, false
	}
	freq := inferFrequency(bestStart, bestEnd)
	if freq == "" {
		return suggestedSettings{}, false
	}
	return suggestedSettings{
		EffectiveDate:        dateOnlyUTC(bestStart),
		FirstReturnPeriodEnd: dateOnlyUTC(bestEnd),
		ReturnFrequency:      freq,
	}, true
}

// obligationsWindow returns the [from, to] query window (YYYY-MM-DD) for the
// reconciliation obligations lookup: a trailing ~year ending a month ahead. It
// stays UNDER HMRC's hard 366-day cap (330 + 30 = 360 days) — exceeding it is a
// hard INVALID_DATE_RANGE rejection. Wide enough to catch the current open
// quarterly/monthly obligation and recent fulfilled ones.
func obligationsWindow(today time.Time) (from, to string) {
	return today.AddDate(0, 0, -330).Format("2006-01-02"), today.AddDate(0, 0, 30).Format("2006-01-02")
}

// periodEndSet builds the set of period-end date strings the generator produces
// for the given settings (empty for incomplete/invalid settings). Used to test
// whether a given period-end is part of a schedule.
func periodEndSet(effective, firstEnd, today time.Time, freq string) map[string]bool {
	set := map[string]bool{}
	for _, p := range generateVATPeriods(effective, firstEnd, today, freq) {
		set[p.End.Format("2006-01-02")] = true
	}
	return set
}

// =============================================================================
// SERVICE METHODS
// =============================================================================

// CheckHMRCPeriods reports whether the org's generated VAT periods line up with
// HMRC's obligations, and (if not) the settings that would make them match. It is
// best-effort and FAILS OPEN: any condition that means "nothing to reconcile" —
// no connection, no VRN, no obligations, or an HMRC error — returns applicable=false
// rather than an error, so a transient HMRC problem never blocks a connect.
// Owner/admin only.
func (s *Service) CheckHMRCPeriods(ctx context.Context, authUserID, authOrgID uuid.UUID) (*VatPeriodCheckResponse, error) {
	role, err := s.authorize(ctx, authUserID, authOrgID)
	if err != nil {
		return nil, err
	}
	if !kernel.IsOrgAdmin(role) {
		return nil, kernel.ErrForbidden("only owners and admins can reconcile VAT periods")
	}

	resp := &VatPeriodCheckResponse{Applicable: false}

	// Need a live HMRC connection.
	if s.hmrc == nil {
		return resp, nil
	}
	if connected, _ := s.hmrc.IsConnected(ctx, authOrgID); !connected {
		return resp, nil
	}

	org, err := s.loadOrg(ctx, authOrgID)
	if err != nil {
		return nil, err
	}
	if !org.VatRegistered || !org.Vrn.Valid || org.Vrn.String == "" {
		return resp, nil // no VRN → nothing to compare against
	}

	accessToken, apiBaseURL, err := s.hmrc.GetToken(ctx, authOrgID)
	if err != nil {
		slog.Warn("vat period-check: could not get HMRC token, skipping reconciliation", "error", err)
		return resp, nil // fail open
	}

	today := dateOnlyUTC(time.Now().UTC())
	from, to := obligationsWindow(today)
	obligations, err := listHMRCObligations(ctx, s.httpClient, apiBaseURL, org.Vrn.String, accessToken, from, to)
	if err != nil {
		slog.Warn("vat period-check: HMRC obligations call failed, skipping reconciliation", "error", err)
		return resp, nil // fail open
	}
	if len(obligations) == 0 {
		return resp, nil // nothing to reconcile
	}

	suggested, ok := deriveSettingsFromObligations(obligations)
	if !ok {
		slog.Warn("vat period-check: could not derive settings from obligations", "count", len(obligations))
		return resp, nil
	}

	resp.Applicable = true
	resp.Suggested = VatPeriodSettings{
		ReturnFrequency:      suggested.ReturnFrequency,
		FirstReturnPeriodEnd: suggested.FirstReturnPeriodEnd.Format("2006-01-02"),
		EffectiveDate:        suggested.EffectiveDate.Format("2006-01-02"),
	}
	if org.VatReturnFrequency.Valid {
		resp.Current.ReturnFrequency = org.VatReturnFrequency.String
	}
	if org.VatFirstReturnPeriodEnd.Valid {
		resp.Current.FirstReturnPeriodEnd = org.VatFirstReturnPeriodEnd.Time.Format("2006-01-02")
	}
	if org.VatEffectiveDate.Valid {
		resp.Current.EffectiveDate = org.VatEffectiveDate.Time.Format("2006-01-02")
	}

	// matches: every HMRC obligation's period-end appears in the schedule the
	// CURRENT settings generate. An incomplete current schedule (empty set) never
	// matches → reconciliation is offered (and fills the settings in).
	currentEnds := map[string]bool{}
	if org.VatRegistered && org.VatEffectiveDate.Valid && org.VatFirstReturnPeriodEnd.Valid && org.VatReturnFrequency.Valid {
		currentEnds = periodEndSet(org.VatEffectiveDate.Time, org.VatFirstReturnPeriodEnd.Time, today, org.VatReturnFrequency.String)
	}
	resp.Matches = true
	for i := range obligations {
		if !currentEnds[obligations[i].End] {
			resp.Matches = false
			break
		}
	}

	// filed_periods_affected: saved returns whose period-end would no longer be a
	// generated period after the rewrite (a warning shown in the confirm modal).
	if !resp.Matches {
		suggestedEnds := periodEndSet(suggested.EffectiveDate, suggested.FirstReturnPeriodEnd, today, suggested.ReturnFrequency)
		summaries, err := s.queries.ListVatReturnSummaries(ctx, authOrgID)
		if err != nil {
			return nil, kernel.ErrInternal(err)
		}
		for _, sm := range summaries {
			if sm.PeriodEnd.Valid && !suggestedEnds[sm.PeriodEnd.Time.Format("2006-01-02")] {
				resp.FiledPeriodsAffected++
			}
		}
	}

	return resp, nil
}

// SyncHMRCPeriods rewrites the org's VAT period settings (effective_date,
// first_return_period_end, return_frequency) to match HMRC's obligations, leaving
// every other VAT field untouched (read-modify-write, like the settings PUT).
// Owner/admin only. Returns the updated settings.
func (s *Service) SyncHMRCPeriods(ctx context.Context, authUserID, authOrgID uuid.UUID) (*VatSettingsResponse, error) {
	role, err := s.authorize(ctx, authUserID, authOrgID)
	if err != nil {
		return nil, err
	}
	if !kernel.IsOrgAdmin(role) {
		return nil, kernel.ErrForbidden("only owners and admins can adjust VAT periods")
	}
	if s.hmrc == nil {
		return nil, kernel.ErrConflict("HMRC connection is not configured")
	}
	if connected, _ := s.hmrc.IsConnected(ctx, authOrgID); !connected {
		return nil, kernel.ErrConflict("this organisation is not connected to HMRC")
	}

	org, err := s.loadOrg(ctx, authOrgID)
	if err != nil {
		return nil, err
	}
	if !org.VatRegistered || !org.Vrn.Valid || org.Vrn.String == "" {
		return nil, kernel.ErrValidation("a VAT registration number (VRN) must be set before syncing periods", nil)
	}

	accessToken, apiBaseURL, err := s.hmrc.GetToken(ctx, authOrgID)
	if err != nil {
		return nil, err
	}

	today := dateOnlyUTC(time.Now().UTC())
	from, to := obligationsWindow(today)
	obligations, err := listHMRCObligations(ctx, s.httpClient, apiBaseURL, org.Vrn.String, accessToken, from, to)
	if err != nil {
		return nil, err
	}
	suggested, ok := deriveSettingsFromObligations(obligations)
	if !ok {
		return nil, kernel.ErrValidation("could not derive VAT periods from HMRC — no usable obligations found", nil)
	}

	// Write ONLY the three period-driving fields; pass every other VAT column
	// through unchanged so the sync can't wipe the VRN, basis, flat-rate, etc.
	updated, err := s.authQueries.UpdateOrganisationVatSettings(ctx, auth.UpdateOrganisationVatSettingsParams{
		ID:                      authOrgID,
		Vrn:                     org.Vrn,
		VatRegistered:           org.VatRegistered,
		VatUsesNonStandardRates: org.VatUsesNonStandardRates,
		VatEffectiveDate:        pgtype.Date{Time: suggested.EffectiveDate, Valid: true},
		VatFirstReturnPeriodEnd: pgtype.Date{Time: suggested.FirstReturnPeriodEnd, Valid: true},
		VatReturnFrequency:      pgtype.Text{String: suggested.ReturnFrequency, Valid: true},
		VatAccountingBasis:      org.VatAccountingBasis,
		VatFlatRateScheme:       org.VatFlatRateScheme,
		VatFlatRateBps:          org.VatFlatRateBps,
		VatPreRegExpenseMonths:  org.VatPreRegExpenseMonths,
	})
	if err != nil {
		return nil, kernel.ErrInternal(err)
	}

	resp := vatSettingsToResponse(updated)
	if connected, connectedAt := s.hmrc.IsConnected(ctx, authOrgID); connected {
		resp.HMRCConnected = true
		if connectedAt != nil {
			ts := connectedAt.Format(time.RFC3339)
			resp.HMRCConnectedAt = &ts
		}
	}
	return resp, nil
}
