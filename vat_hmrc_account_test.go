package main

// vat_hmrc_account_test.go
// =============================================================================
// Integration tests for the VAT dashboard read layer (the HMRC MTD VAT-account
// GET endpoints): obligations, view-return, liabilities, payments, penalties,
// financial-details, information.
//
// Per the project rule we use REAL Postgres (for the org/membership/VRN) and fake
// ONLY the external service: an httptest.Server returns canned HMRC JSON, and a
// fake HMRCConnector points the vat.Service's token vend at that server's URL. We
// build the vat.Service directly (rather than through the wired router) so the fake
// connector is injected without touching main's wiring; a separate test proves the
// HTTP routes are registered and behind auth.
//
// Coverage: happy-path parsing for all seven reads, money conversion (HMRC decimal
// → our 2-dp string, no float drift), the date-window 366-day cap, and the guards
// (no VRN → 422, not connected → 409, non-member → rejected).
// =============================================================================

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	authdb "github.com/operationfb/accounting-saas/db/auth"
	vatdb "github.com/operationfb/accounting-saas/db/vat"
	"github.com/operationfb/accounting-saas/internal/kernel"
	vat "github.com/operationfb/accounting-saas/internal/vat"
)

// fakeHMRC satisfies vat.HMRCConnector. GetToken hands back the httptest server URL
// as the "API base URL", so the vat.Service's real HTTP client calls our fake. With
// err set, GetToken fails (the not-connected path is covered separately with nil).
type fakeHMRC struct {
	base string
	err  error
}

func (f *fakeHMRC) IsConnected(ctx context.Context, orgID uuid.UUID) (bool, *time.Time) {
	if f.err != nil {
		return false, nil
	}
	now := time.Now()
	return true, &now
}

func (f *fakeHMRC) GetToken(ctx context.Context, orgID uuid.UUID) (string, string, error) {
	if f.err != nil {
		return "", "", f.err
	}
	return "test-access-token", f.base, nil
}

// Canned HMRC responses. Money crosses as JSON numbers (pounds), exactly as HMRC
// sends it — the service must format these to fixed-dp strings without float drift.
const (
	obligationsJSON = `{"obligations":[
		{"periodKey":"24A2","start":"2026-04-01","end":"2026-06-30","due":"2026-08-07","status":"O"},
		{"periodKey":"24A1","start":"2026-01-01","end":"2026-03-31","due":"2026-05-07","status":"F","received":"2026-05-02"}
	]}`
	viewReturnJSON = `{"periodKey":"24A1","vatDueSales":4968.00,"vatDueAcquisitions":0,
		"totalVatDue":4968.00,"vatReclaimedCurrPeriod":1243.00,"netVatDue":3725.00,
		"totalValueSalesExVAT":24840,"totalValuePurchasesExVAT":6215,
		"totalValueGoodsSuppliedExVAT":0,"totalAcquisitionsExVAT":0}`
	liabilitiesJSON = `{"liabilities":[
		{"taxPeriod":{"from":"2026-01-01","to":"2026-03-31"},"type":"VAT",
		 "originalAmount":3102.00,"outstandingAmount":0,"due":"2026-05-07"}
	]}`
	paymentsJSON = `{"payments":[
		{"amount":3102.00,"received":"2026-05-03"},
		{"amount":2874.00,"received":"2026-02-04"}
	]}`
	penaltiesJSON = `{"totalisations":{"LSPTotalValue":200.00,"LPPPostedTotal":15.50},
		"lateSubmissionPenalty":{"summary":{"activePenaltyPoints":1,"inactivePenaltyPoints":0,
		  "regimeThreshold":4,"penaltyChargeAmount":0},
		  "details":[{"penaltyChargeReference":"XM002610011594","penaltyCategory":"P",
		    "penaltyStatus":"ACTIVE","chargeAmount":0}]},
		"latePaymentPenalty":{"details":[{"penaltyChargeReference":"XM002610011595",
		  "penaltyCategory":"LPP1","penaltyStatus":"ACTIVE","penaltyAmountOutstanding":15.50}]}}`
	financialDetailsJSON = `{"documentDetails":[{"documentType":"VAT Late Payment Penalty",
		"chargeReferenceNumber":"XM002610011595","documentTotalAmount":15.50,
		"documentOutstandingAmount":15.50,"documentDueDate":"2026-06-07"}]}`
	informationJSON = `{"organisationName":"Acme Trading Ltd","tradingName":"Acme",
		"businessAddress":{"line1":"14 King St","line2":"","postcode":"M2 6AG","countryCode":"GB"},
		"registrationDate":"2022-04-01"}`
)

// fakeHMRCServer routes by path suffix to the canned response. It also asserts the
// bearer + vendor Accept headers our client must send.
func fakeHMRCServer(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-access-token" {
			t.Errorf("missing/wrong bearer token on %s: %q", r.URL.Path, r.Header.Get("Authorization"))
		}
		if r.Header.Get("Accept") != "application/vnd.hmrc.1.0+json" {
			t.Errorf("missing HMRC Accept header on %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		p := r.URL.Path
		switch {
		case strings.HasSuffix(p, "/obligations"):
			_, _ = w.Write([]byte(obligationsJSON))
		case strings.Contains(p, "/returns/"):
			_, _ = w.Write([]byte(viewReturnJSON))
		case strings.HasSuffix(p, "/liabilities"):
			_, _ = w.Write([]byte(liabilitiesJSON))
		case strings.HasSuffix(p, "/payments"):
			_, _ = w.Write([]byte(paymentsJSON))
		case strings.HasSuffix(p, "/penalties"):
			_, _ = w.Write([]byte(penaltiesJSON))
		case strings.Contains(p, "/financial-details/"):
			_, _ = w.Write([]byte(financialDetailsJSON))
		case strings.HasSuffix(p, "/information"):
			_, _ = w.Write([]byte(informationJSON))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
}

// newVatDashboardService builds a vat.Service over the shared pool, with the fake
// HMRC connector pointed at hmrcBase.
func newVatDashboardService(ts *testServer, hmrcBase string) *vat.Service {
	return vat.NewService(authdb.New(ts.pool), vatdb.New(ts.pool), &fakeHMRC{base: hmrcBase})
}

func TestHMRCDashboard(t *testing.T) {
	t.Parallel()
	ts := newTestServer(t)
	t.Cleanup(func() { ts.pool.Close() })

	orgID, ownerID := newOrgWithOwner(t, ts)
	authHeader := bearer(t, ts, ownerID, orgID)
	// registeredBody sets the VRN ("GB 123 456 789" → 123456789) + vat_registered.
	if rec := putVatSettings(t, ts, authHeader, registeredBody()); rec.Code != http.StatusOK {
		t.Fatalf("register VAT settings: %d — %s", rec.Code, rec.Body.String())
	}

	srv := fakeHMRCServer(t)
	defer srv.Close()
	svc := newVatDashboardService(ts, srv.URL)

	ctx := context.Background()
	org := uuid.MustParse(orgID)
	owner := uuid.MustParse(ownerID)

	t.Run("obligations parse + status filter", func(t *testing.T) {
		obs, err := svc.GetHMRCObligations(ctx, owner, org, "", "", "")
		if err != nil {
			t.Fatalf("GetHMRCObligations: %v", err)
		}
		if len(obs) != 2 {
			t.Fatalf("want 2 obligations, got %d", len(obs))
		}
		if obs[0].PeriodKey != "24A2" || obs[0].Status != "O" || obs[0].Due != "2026-08-07" {
			t.Errorf("open obligation wrong: %+v", obs[0])
		}
		if obs[1].Status != "F" || obs[1].Received == nil || *obs[1].Received != "2026-05-02" {
			t.Errorf("fulfilled obligation should carry received date: %+v", obs[1])
		}
		// Status filter applied in Go.
		f, err := svc.GetHMRCObligations(ctx, owner, org, "", "", "F")
		if err != nil {
			t.Fatalf("filtered: %v", err)
		}
		if len(f) != 1 || f[0].Status != "F" {
			t.Errorf("status=F should yield 1 fulfilled, got %d", len(f))
		}
		// A bad status is rejected.
		if _, err := svc.GetHMRCObligations(ctx, owner, org, "", "", "X"); err == nil {
			t.Error("status=X should be rejected")
		}
	})

	t.Run("view return: money is exact strings, no float", func(t *testing.T) {
		r, err := svc.GetHMRCReturn(ctx, owner, org, "24A1")
		if err != nil {
			t.Fatalf("GetHMRCReturn: %v", err)
		}
		want := map[string]string{
			"box1": "4968.00", "box4": "1243.00", "box5": "3725.00",
			"box6": "24840", "box7": "6215", "box2": "0.00",
		}
		got := map[string]string{
			"box1": r.Box1, "box4": r.Box4, "box5": r.Box5,
			"box6": r.Box6, "box7": r.Box7, "box2": r.Box2,
		}
		for k, v := range want {
			if got[k] != v {
				t.Errorf("%s: got %q, want %q", k, got[k], v)
			}
		}
	})

	t.Run("liabilities", func(t *testing.T) {
		ls, err := svc.GetHMRCLiabilities(ctx, owner, org, "", "")
		if err != nil {
			t.Fatalf("GetHMRCLiabilities: %v", err)
		}
		if len(ls) != 1 {
			t.Fatalf("want 1 liability, got %d", len(ls))
		}
		l := ls[0]
		if l.Type != "VAT" || l.OriginalAmount != "3102.00" || l.OutstandingAmount != "0.00" {
			t.Errorf("liability amounts wrong: %+v", l)
		}
		if l.From == nil || *l.From != "2026-01-01" || l.Due == nil || *l.Due != "2026-05-07" {
			t.Errorf("liability period/due wrong: %+v", l)
		}
	})

	t.Run("payments", func(t *testing.T) {
		ps, err := svc.GetHMRCPayments(ctx, owner, org, "", "")
		if err != nil {
			t.Fatalf("GetHMRCPayments: %v", err)
		}
		if len(ps) != 2 || ps[0].Amount != "3102.00" || ps[0].Received == nil || *ps[0].Received != "2026-05-03" {
			t.Errorf("payments wrong: %+v", ps)
		}
	})

	t.Run("penalties + points + total", func(t *testing.T) {
		p, err := svc.GetHMRCPenalties(ctx, owner, org)
		if err != nil {
			t.Fatalf("GetHMRCPenalties: %v", err)
		}
		if p.ActivePoints != 1 || p.Threshold != 4 {
			t.Errorf("points: got active=%d threshold=%d, want 1/4", p.ActivePoints, p.Threshold)
		}
		if p.TotalPenalties != "215.50" {
			t.Errorf("total penalties: got %q, want 215.50 (200.00 + 15.50)", p.TotalPenalties)
		}
		if len(p.Penalties) != 2 {
			t.Fatalf("want 2 penalty charges (LSP+LPP), got %d", len(p.Penalties))
		}
	})

	t.Run("financial details", func(t *testing.T) {
		fd, err := svc.GetHMRCFinancialDetails(ctx, owner, org, "XM002610011595")
		if err != nil {
			t.Fatalf("GetHMRCFinancialDetails: %v", err)
		}
		if fd.ChargeReference != "XM002610011595" || len(fd.Documents) != 1 {
			t.Fatalf("financial details wrong: %+v", fd)
		}
		if fd.Documents[0].TotalAmount != "15.50" || fd.Documents[0].OutstandingAmount != "15.50" {
			t.Errorf("document amounts wrong: %+v", fd.Documents[0])
		}
	})

	t.Run("information", func(t *testing.T) {
		inf, err := svc.GetHMRCInformation(ctx, owner, org)
		if err != nil {
			t.Fatalf("GetHMRCInformation: %v", err)
		}
		if inf.BusinessName != "Acme Trading Ltd" || inf.Postcode != "M2 6AG" || inf.CountryCode != "GB" {
			t.Errorf("information wrong: %+v", inf)
		}
		// Blank address lines are dropped (line2 was "").
		if len(inf.AddressLines) != 1 || inf.AddressLines[0] != "14 King St" {
			t.Errorf("address lines wrong: %+v", inf.AddressLines)
		}
	})

	t.Run("date window over 366 days → 422", func(t *testing.T) {
		_, err := svc.GetHMRCLiabilities(ctx, owner, org, "2024-01-01", "2026-01-01")
		assertAppCode(t, err, kernel.ErrCodeValidation)
	})

	t.Run("multi-tenant: a non-member is rejected", func(t *testing.T) {
		_, otherOwnerID := newOrgWithOwner(t, ts) // owner of a DIFFERENT org
		otherOwner := uuid.MustParse(otherOwnerID)
		if _, err := svc.GetHMRCObligations(ctx, otherOwner, org, "", "", ""); err == nil {
			t.Error("a user who is not a member of the org must be rejected")
		}
	})
}

// TestHMRCDashboardGuards covers the two pre-flight guards in hmrcAccess.
func TestHMRCDashboardGuards(t *testing.T) {
	t.Parallel()
	ts := newTestServer(t)
	t.Cleanup(func() { ts.pool.Close() })

	orgID, ownerID := newOrgWithOwner(t, ts)
	org := uuid.MustParse(orgID)
	owner := uuid.MustParse(ownerID)
	ctx := context.Background()

	srv := fakeHMRCServer(t)
	defer srv.Close()

	t.Run("no VRN set → 422", func(t *testing.T) {
		// The org is not VAT-registered yet (no VRN).
		svc := newVatDashboardService(ts, srv.URL)
		_, err := svc.GetHMRCObligations(ctx, owner, org, "", "", "")
		assertAppCode(t, err, kernel.ErrCodeValidation)
	})

	t.Run("VRN set but HMRC not connected → 409", func(t *testing.T) {
		authHeader := bearer(t, ts, ownerID, orgID)
		if rec := putVatSettings(t, ts, authHeader, registeredBody()); rec.Code != http.StatusOK {
			t.Fatalf("register: %d — %s", rec.Code, rec.Body.String())
		}
		// nil connector → "HMRC connection is not configured" (409).
		svc := vat.NewService(authdb.New(ts.pool), vatdb.New(ts.pool), nil)
		_, err := svc.GetHMRCObligations(ctx, owner, org, "", "", "")
		assertAppCode(t, err, kernel.ErrCodeConflict)
	})
}

// TestHMRCDashboardRoutesRequireAuth proves the dashboard routes are registered on
// the real router and sit behind the bearer-token middleware (no token → 401).
func TestHMRCDashboardRoutesRequireAuth(t *testing.T) {
	t.Parallel()
	ts := newTestServer(t)
	t.Cleanup(func() { ts.pool.Close() })

	paths := []string{
		"/api/v1/vat/hmrc/obligations",
		"/api/v1/vat/hmrc/returns/24A1",
		"/api/v1/vat/hmrc/liabilities",
		"/api/v1/vat/hmrc/payments",
		"/api/v1/vat/hmrc/penalties",
		"/api/v1/vat/hmrc/financial-details/XM002610011595",
		"/api/v1/vat/hmrc/information",
	}
	for _, p := range paths {
		rec := httptest.NewRecorder()
		req, _ := http.NewRequest(http.MethodGet, p, nil)
		ts.server.router.ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("%s without a token: want 401, got %d", p, rec.Code)
		}
	}
}
