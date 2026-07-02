package main

// vat_settings_test.go
// =============================================================================
// Integration tests for the VAT Registration settings endpoints
// (GET/PUT /api/v1/vat/settings) — the "UK VAT Registration" screen.
//
// Like the rest of the suite these hit a REAL PostgreSQL database via the shared
// newTestServer harness (server_test.go) and skip cleanly when DATABASE_URL is
// unset. They run against a throwaway org+owner from newOrgWithOwner (whose rows
// are deleted on cleanup), so the shared dev org is never mutated — the same
// approach as user_service_test.go / organisation_service_test.go.
//
// Coverage:
//   - GET: defaults for a fresh org; 401 unauthenticated.
//   - PUT happy path: the registered round-trip — incl. VRN normalisation
//     ("GB 123 456 789" → "123456789"), the flat-rate %↔bps conversion, re-GET,
//     and DB persistence (only a real DB can prove the row committed).
//   - Deregistering clears the certificate fields (read-modify-write via the
//     focused query).
//   - Validation: service 422 (bad VRN; a missing certificate field while
//     registered) and binding 400 (an invalid return_frequency enum).
//   - AuthZ: owner/admin-only PUT (a plain member → 403, but member GET → 200);
//     401 unauthenticated.
//   - Multi-tenant isolation: org A's save never leaks into org B.
// =============================================================================

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	vat "github.com/operationfb/accounting-saas/internal/vat"
)

// =============================================================================
// VAT SETTINGS TEST HELPERS
// =============================================================================

// getVatSettingsReq sends GET /api/v1/vat/settings with the given auth header
// (empty = none).
func getVatSettingsReq(t *testing.T, ts *testServer, authHeader string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodGet, "/api/v1/vat/settings", nil)
	if authHeader != "" {
		req.Header.Set("Authorization", authHeader)
	}
	ts.server.router.ServeHTTP(rec, req)
	return rec
}

// putVatSettings sends PUT /api/v1/vat/settings with a typed request body.
func putVatSettings(t *testing.T, ts *testServer, authHeader string, body vat.VatSettingsRequest) *httptest.ResponseRecorder {
	t.Helper()
	bodyBytes, _ := json.Marshal(body)
	return putVatSettingsRaw(t, ts, authHeader, string(bodyBytes))
}

// putVatSettingsRaw sends a raw JSON body — for binding-level cases the typed
// struct can't express (e.g. an out-of-set return_frequency enum).
func putVatSettingsRaw(t *testing.T, ts *testServer, authHeader, rawJSON string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodPut, "/api/v1/vat/settings", bytes.NewReader([]byte(rawJSON)))
	req.Header.Set("Content-Type", "application/json")
	if authHeader != "" {
		req.Header.Set("Authorization", authHeader)
	}
	ts.server.router.ServeHTTP(rec, req)
	return rec
}

// decodeVatSettings pulls the { "vat_settings": {...} } envelope into the response DTO.
func decodeVatSettings(t *testing.T, body []byte) vat.VatSettingsResponse {
	t.Helper()
	var resp struct {
		VatSettings vat.VatSettingsResponse `json:"vat_settings"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		t.Fatalf("decode vat_settings: %v — body: %s", err, string(body))
	}
	return resp.VatSettings
}

// registeredBody is a complete, valid "registered" payload used by several tests.
// VRN is deliberately given with a "GB" prefix + spaces to exercise normalisation.
func registeredBody() vat.VatSettingsRequest {
	return vat.VatSettingsRequest{
		VatRegistered:        true,
		Vrn:                  strPtr("GB 123 456 789"),
		UsesNonStandardRates: false,
		EffectiveDate:        strPtr("2026-03-01"),
		FirstReturnPeriodEnd: strPtr("2026-05-31"),
		ReturnFrequency:      strPtr("quarterly"),
		AccountingBasis:      strPtr("invoice"),
		FlatRateScheme:       false,
		PreRegExpenseMonths:  ptr(int32(6)),
	}
}

// getVatPeriodsReq sends GET /api/v1/vat/periods.
func getVatPeriodsReq(t *testing.T, ts *testServer, authHeader string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodGet, "/api/v1/vat/periods", nil)
	if authHeader != "" {
		req.Header.Set("Authorization", authHeader)
	}
	ts.server.router.ServeHTTP(rec, req)
	return rec
}

// decodeVatPeriods pulls the { "periods": [...] } envelope.
func decodeVatPeriods(t *testing.T, body []byte) []vat.VatPeriodResponse {
	t.Helper()
	var resp struct {
		Periods []vat.VatPeriodResponse `json:"periods"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		t.Fatalf("decode periods: %v — body: %s", err, string(body))
	}
	return resp.Periods
}

// =============================================================================
// GET
// =============================================================================

// TestHandleGetVatSettings covers GET /api/v1/vat/settings.
func TestHandleGetVatSettings(t *testing.T) {
	t.Parallel()
	ts := newTestServer(t)
	t.Cleanup(func() { ts.pool.Close() })

	t.Run("fresh org returns defaults", func(t *testing.T) {
		orgID, ownerID := newOrgWithOwner(t, ts)

		rec := getVatSettingsReq(t, ts, bearer(t, ts, ownerID, orgID))
		if rec.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d — body: %s", rec.Code, rec.Body.String())
		}
		got := decodeVatSettings(t, rec.Body.Bytes())

		if got.VatRegistered || got.UsesNonStandardRates || got.FlatRateScheme {
			t.Errorf("fresh org should have all toggles false, got %+v", got)
		}
		// Optional fields are NULL → nil pointers in the response.
		if got.Vrn != nil || got.EffectiveDate != nil || got.FirstReturnPeriodEnd != nil ||
			got.ReturnFrequency != nil || got.AccountingBasis != nil ||
			got.FlatRatePercentage != nil || got.PreRegExpenseMonths != nil {
			t.Errorf("fresh org should have nil optional fields, got %+v", got)
		}
	})

	t.Run("unauthenticated → 401", func(t *testing.T) {
		rec := getVatSettingsReq(t, ts, "")
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("expected 401, got %d — body: %s", rec.Code, rec.Body.String())
		}
	})
}

// =============================================================================
// UPDATE — happy paths
// =============================================================================

// TestHandleUpdateVatSettings covers the PUT happy paths + persistence.
func TestHandleUpdateVatSettings(t *testing.T) {
	t.Parallel()
	ts := newTestServer(t)
	t.Cleanup(func() { ts.pool.Close() })

	t.Run("registered round-trip: normalises VRN, persists, re-GET reflects", func(t *testing.T) {
		orgID, ownerID := newOrgWithOwner(t, ts)
		authHeader := bearer(t, ts, ownerID, orgID)

		rec := putVatSettings(t, ts, authHeader, registeredBody())
		if rec.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d — body: %s", rec.Code, rec.Body.String())
		}
		got := decodeVatSettings(t, rec.Body.Bytes())

		if !got.VatRegistered {
			t.Error("vat_registered: got false, want true")
		}
		// "GB 123 456 789" → bare 9 digits.
		if got.Vrn == nil || *got.Vrn != "123456789" {
			t.Errorf("vrn: got %v, want %q (GB prefix + spaces stripped)", got.Vrn, "123456789")
		}
		if got.EffectiveDate == nil || *got.EffectiveDate != "2026-03-01" {
			t.Errorf("effective_date: got %v, want %q", got.EffectiveDate, "2026-03-01")
		}
		if got.FirstReturnPeriodEnd == nil || *got.FirstReturnPeriodEnd != "2026-05-31" {
			t.Errorf("first_return_period_end: got %v, want %q", got.FirstReturnPeriodEnd, "2026-05-31")
		}
		if got.ReturnFrequency == nil || *got.ReturnFrequency != "quarterly" {
			t.Errorf("return_frequency: got %v, want %q", got.ReturnFrequency, "quarterly")
		}
		if got.AccountingBasis == nil || *got.AccountingBasis != "invoice" {
			t.Errorf("accounting_basis: got %v, want %q", got.AccountingBasis, "invoice")
		}
		if got.PreRegExpenseMonths == nil || *got.PreRegExpenseMonths != 6 {
			t.Errorf("pre_reg_expense_months: got %v, want 6", got.PreRegExpenseMonths)
		}

		// Persisted across a fresh read.
		reread := decodeVatSettings(t, getVatSettingsReq(t, ts, authHeader).Body.Bytes())
		if reread.Vrn == nil || *reread.Vrn != "123456789" || !reread.VatRegistered {
			t.Errorf("re-GET did not reflect the save: %+v", reread)
		}

		// Row actually committed — only a real DB can prove this. The DATE is cast to
		// text so it scans into a plain string.
		var dbVrn, dbFreq, dbEffective string
		var dbRegistered bool
		if err := ts.pool.QueryRow(context.Background(),
			"SELECT vrn, vat_registered, vat_effective_date::text, vat_return_frequency FROM organisations WHERE id = $1", orgID,
		).Scan(&dbVrn, &dbRegistered, &dbEffective, &dbFreq); err != nil {
			t.Fatalf("re-read org: %v", err)
		}
		if dbVrn != "123456789" || !dbRegistered || dbEffective != "2026-03-01" || dbFreq != "quarterly" {
			t.Errorf("DB row mismatch: vrn=%q registered=%v effective=%q freq=%q", dbVrn, dbRegistered, dbEffective, dbFreq)
		}
	})

	// The flat-rate percentage crosses the API as a percentage string but is stored
	// as basis points (the repo's rate convention). This guards that conversion both
	// ways: "12.5" → 1250 in the DB → "12.5" back out.
	t.Run("flat-rate percentage ↔ bps conversion", func(t *testing.T) {
		orgID, ownerID := newOrgWithOwner(t, ts)
		authHeader := bearer(t, ts, ownerID, orgID)

		body := registeredBody()
		body.FlatRateScheme = true
		body.FlatRatePercentage = strPtr("12.5")

		rec := putVatSettings(t, ts, authHeader, body)
		if rec.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d — body: %s", rec.Code, rec.Body.String())
		}
		got := decodeVatSettings(t, rec.Body.Bytes())
		if !got.FlatRateScheme {
			t.Error("flat_rate_scheme: got false, want true")
		}
		if got.FlatRatePercentage == nil || *got.FlatRatePercentage != "12.5" {
			t.Errorf("flat_rate_percentage: got %v, want %q", got.FlatRatePercentage, "12.5")
		}

		var dbBps int32
		if err := ts.pool.QueryRow(context.Background(),
			"SELECT vat_flat_rate_bps FROM organisations WHERE id = $1", orgID).Scan(&dbBps); err != nil {
			t.Fatalf("re-read flat-rate bps: %v", err)
		}
		if dbBps != 1250 {
			t.Errorf("vat_flat_rate_bps: got %d, want 1250 (12.5%% as basis points)", dbBps)
		}
	})

	// Deregistering with empty fields clears the certificate columns — proving the
	// focused query's read-modify-write actually NULLs them, not just the toggle.
	t.Run("deregistering clears the certificate fields", func(t *testing.T) {
		orgID, ownerID := newOrgWithOwner(t, ts)
		authHeader := bearer(t, ts, ownerID, orgID)

		// First register with full data.
		if rec := putVatSettings(t, ts, authHeader, registeredBody()); rec.Code != http.StatusOK {
			t.Fatalf("register: expected 200, got %d — body: %s", rec.Code, rec.Body.String())
		}
		// Then flip to not-registered with everything else blank.
		rec := putVatSettings(t, ts, authHeader, vat.VatSettingsRequest{VatRegistered: false})
		if rec.Code != http.StatusOK {
			t.Fatalf("deregister: expected 200, got %d — body: %s", rec.Code, rec.Body.String())
		}
		got := decodeVatSettings(t, rec.Body.Bytes())
		if got.VatRegistered {
			t.Error("vat_registered: got true, want false")
		}
		if got.Vrn != nil || got.EffectiveDate != nil || got.ReturnFrequency != nil {
			t.Errorf("certificate fields should be cleared, got %+v", got)
		}
	})
}

// =============================================================================
// UPDATE — validation
// =============================================================================

func TestHandleUpdateVatSettingsValidation(t *testing.T) {
	t.Parallel()
	ts := newTestServer(t)
	t.Cleanup(func() { ts.pool.Close() })

	t.Run("registered + bad VRN → 422", func(t *testing.T) {
		orgID, ownerID := newOrgWithOwner(t, ts)
		body := registeredBody()
		body.Vrn = strPtr("12345") // not 9 digits
		rec := putVatSettings(t, ts, bearer(t, ts, ownerID, orgID), body)
		if rec.Code != http.StatusUnprocessableEntity {
			t.Errorf("expected 422, got %d — body: %s", rec.Code, rec.Body.String())
		}
	})

	t.Run("registered + missing effective_date → 422", func(t *testing.T) {
		orgID, ownerID := newOrgWithOwner(t, ts)
		body := registeredBody()
		body.EffectiveDate = nil // valid VRN, but a required cert field is missing
		rec := putVatSettings(t, ts, bearer(t, ts, ownerID, orgID), body)
		if rec.Code != http.StatusUnprocessableEntity {
			t.Errorf("expected 422, got %d — body: %s", rec.Code, rec.Body.String())
		}
	})

	// An out-of-set enum is caught by the binding `oneof` tag → 400 (before the service).
	t.Run("invalid return_frequency → 400 binding", func(t *testing.T) {
		orgID, ownerID := newOrgWithOwner(t, ts)
		rec := putVatSettingsRaw(t, ts, bearer(t, ts, ownerID, orgID),
			`{"vat_registered":false,"uses_non_standard_rates":false,"flat_rate_scheme":false,"return_frequency":"weekly"}`)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("expected 400, got %d — body: %s", rec.Code, rec.Body.String())
		}
	})

	t.Run("not-registered saves without requiring certificate fields", func(t *testing.T) {
		orgID, ownerID := newOrgWithOwner(t, ts)
		rec := putVatSettings(t, ts, bearer(t, ts, ownerID, orgID), vat.VatSettingsRequest{VatRegistered: false})
		if rec.Code != http.StatusOK {
			t.Errorf("expected 200, got %d — body: %s", rec.Code, rec.Body.String())
		}
	})
}

// =============================================================================
// UPDATE — authorization
// =============================================================================

func TestVatSettingsAuthorization(t *testing.T) {
	t.Parallel()
	ts := newTestServer(t)
	t.Cleanup(func() { ts.pool.Close() })

	t.Run("plain member may GET but not PUT (403)", func(t *testing.T) {
		orgID, _ := newOrgWithOwner(t, ts)
		memberID := newMemberUser(t, ts, orgID)
		authHeader := bearer(t, ts, memberID, orgID)

		// Read is allowed for any active member.
		if rec := getVatSettingsReq(t, ts, authHeader); rec.Code != http.StatusOK {
			t.Errorf("member GET: expected 200, got %d — body: %s", rec.Code, rec.Body.String())
		}
		// Edit is owner/admin only.
		if rec := putVatSettings(t, ts, authHeader, registeredBody()); rec.Code != http.StatusForbidden {
			t.Errorf("member PUT: expected 403, got %d — body: %s", rec.Code, rec.Body.String())
		}
	})

	t.Run("unauthenticated PUT → 401", func(t *testing.T) {
		rec := putVatSettings(t, ts, "", registeredBody())
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("expected 401, got %d — body: %s", rec.Code, rec.Body.String())
		}
	})
}

// =============================================================================
// MULTI-TENANT ISOLATION
// =============================================================================

// TestVatSettingsMultiTenantIsolation proves a save scoped to org A (from A's
// token) never touches org B's row. Isolation is inherent — the org comes from the
// token, with no id to pass — but this guards it explicitly.
func TestVatSettingsMultiTenantIsolation(t *testing.T) {
	t.Parallel()
	ts := newTestServer(t)
	t.Cleanup(func() { ts.pool.Close() })

	orgA, ownerA := newOrgWithOwner(t, ts)
	orgB, ownerB := newOrgWithOwner(t, ts)

	// Register org A with a distinctive VRN.
	bodyA := registeredBody()
	bodyA.Vrn = strPtr("111111111")
	if rec := putVatSettings(t, ts, bearer(t, ts, ownerA, orgA), bodyA); rec.Code != http.StatusOK {
		t.Fatalf("org A register: expected 200, got %d — body: %s", rec.Code, rec.Body.String())
	}

	// Org B is untouched — still at defaults.
	gotB := decodeVatSettings(t, getVatSettingsReq(t, ts, bearer(t, ts, ownerB, orgB)).Body.Bytes())
	if gotB.VatRegistered || gotB.Vrn != nil {
		t.Errorf("org B leaked org A's save: %+v", gotB)
	}

	// Org A reads back its own value.
	gotA := decodeVatSettings(t, getVatSettingsReq(t, ts, bearer(t, ts, ownerA, orgA)).Body.Bytes())
	if gotA.Vrn == nil || *gotA.Vrn != "111111111" {
		t.Errorf("org A vrn: got %v, want %q", gotA.Vrn, "111111111")
	}
}

// =============================================================================
// PERIODS (GET /api/v1/vat/periods)
// =============================================================================

// TestHandleListVatPeriods covers the generated period schedule. Assertions are
// robust to the wall-clock "today" (the number of elapsed periods grows over time):
// it checks the OLDEST period — the first return, always present once today is past
// the effective date — and the ended↔status invariant, rather than a fixed count.
func TestHandleListVatPeriods(t *testing.T) {
	t.Parallel()
	ts := newTestServer(t)
	t.Cleanup(func() { ts.pool.Close() })

	t.Run("registered org generates periods, newest-first, with deadlines", func(t *testing.T) {
		orgID, ownerID := newOrgWithOwner(t, ts)
		authHeader := bearer(t, ts, ownerID, orgID)
		// Register: effective 2026-03-01, first return ends 2026-05-31, quarterly.
		if rec := putVatSettings(t, ts, authHeader, registeredBody()); rec.Code != http.StatusOK {
			t.Fatalf("register: expected 200, got %d — body: %s", rec.Code, rec.Body.String())
		}

		rec := getVatPeriodsReq(t, ts, authHeader)
		if rec.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d — body: %s", rec.Code, rec.Body.String())
		}
		periods := decodeVatPeriods(t, rec.Body.Bytes())
		if len(periods) == 0 {
			t.Fatal("expected at least one period for a registered org")
		}

		// Newest-first: end dates non-increasing. And the ended↔status invariant
		// holds for every row (time-independent).
		for i, p := range periods {
			if i > 0 && periods[i-1].EndDate < p.EndDate {
				t.Errorf("periods not newest-first: %q before %q", periods[i-1].EndDate, p.EndDate)
			}
			wantStatus := "Open"
			if p.Ended {
				wantStatus = "Unfiled"
			}
			if p.DisplayStatus != wantStatus {
				t.Errorf("period %s: status %q, want %q (ended=%v)", p.EndDate, p.DisplayStatus, wantStatus, p.Ended)
			}
		}

		// The OLDEST period (last in the list) is the first return: Mar1–May31, due
		// 7 Jul, labelled "05 26" — fixed by the settings, independent of "today".
		oldest := periods[len(periods)-1]
		if oldest.StartDate != "2026-03-01" || oldest.EndDate != "2026-05-31" {
			t.Errorf("first period: got %s–%s, want 2026-03-01–2026-05-31", oldest.StartDate, oldest.EndDate)
		}
		if oldest.DueOn != "2026-07-07" {
			t.Errorf("first period due_on: got %q, want 2026-07-07", oldest.DueOn)
		}
		if oldest.Label != "05 26" {
			t.Errorf("first period label: got %q, want %q", oldest.Label, "05 26")
		}
	})

	t.Run("not-registered org → empty list", func(t *testing.T) {
		orgID, ownerID := newOrgWithOwner(t, ts)
		rec := getVatPeriodsReq(t, ts, bearer(t, ts, ownerID, orgID))
		if rec.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d — body: %s", rec.Code, rec.Body.String())
		}
		if periods := decodeVatPeriods(t, rec.Body.Bytes()); len(periods) != 0 {
			t.Errorf("expected empty list for a not-registered org, got %d periods", len(periods))
		}
	})

	t.Run("plain member may read", func(t *testing.T) {
		orgID, ownerID := newOrgWithOwner(t, ts)
		if rec := putVatSettings(t, ts, bearer(t, ts, ownerID, orgID), registeredBody()); rec.Code != http.StatusOK {
			t.Fatalf("register: %d — %s", rec.Code, rec.Body.String())
		}
		memberID := newMemberUser(t, ts, orgID)
		rec := getVatPeriodsReq(t, ts, bearer(t, ts, memberID, orgID))
		if rec.Code != http.StatusOK {
			t.Errorf("member GET periods: expected 200, got %d — body: %s", rec.Code, rec.Body.String())
		}
	})

	t.Run("unauthenticated → 401", func(t *testing.T) {
		rec := getVatPeriodsReq(t, ts, "")
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("expected 401, got %d", rec.Code)
		}
	})
}
