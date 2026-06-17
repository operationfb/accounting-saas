package main

// organisation_service_test.go
// =============================================================================
// Integration tests for the organisation "Company Details" endpoints
// (GET/PUT /api/v1/organisation).
//
// Like the rest of the suite these hit a REAL PostgreSQL database via the shared
// newTestServer harness (server_test.go) and skip cleanly when DATABASE_URL is
// unset. To avoid polluting the shared dev organisation (which other tests read),
// the mutating tests run against a throwaway org created by newOrgWithOwner —
// whose owner has the 'owner' role (an admin) — and add a 'member' via
// newMemberUser for the non-admin cases. Both helpers clean up after themselves.
//
// Coverage: update happy path + field round-trip + re-GET + DB persistence;
// read by a non-admin member; owner/admin-only editing (member & non-member 403);
// validation (binding 400 + service-layer 422); the read-modify-write preservation
// of fields this form does not own (slug / native_currency / timezone / vrn); and
// multi-tenant isolation. There is no money here, so no decimal-conversion test.
// =============================================================================

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// =============================================================================
// ORGANISATION TEST HELPERS
// =============================================================================

// putOrganisation sends PUT /api/v1/organisation with the given auth header
// (empty = none) and JSON body, returning the recorder.
func putOrganisation(t *testing.T, ts *testServer, authHeader string, body UpdateOrganisationRequest) *httptest.ResponseRecorder {
	t.Helper()
	bodyBytes, _ := json.Marshal(body)
	rec := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodPut, "/api/v1/organisation", bytes.NewReader(bodyBytes))
	req.Header.Set("Content-Type", "application/json")
	if authHeader != "" {
		req.Header.Set("Authorization", authHeader)
	}
	ts.server.router.ServeHTTP(rec, req)
	return rec
}

// getOrganisationReq sends GET /api/v1/organisation.
func getOrganisationReq(t *testing.T, ts *testServer, authHeader string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodGet, "/api/v1/organisation", nil)
	if authHeader != "" {
		req.Header.Set("Authorization", authHeader)
	}
	ts.server.router.ServeHTTP(rec, req)
	return rec
}

// decodeOrganisation pulls the { "organisation": {...} } envelope into a response.
func decodeOrganisation(t *testing.T, body []byte) OrganisationDetailsResponse {
	t.Helper()
	var resp struct {
		Organisation OrganisationDetailsResponse `json:"organisation"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		t.Fatalf("decode organisation: %v — body: %s", err, string(body))
	}
	return resp.Organisation
}

// assertOrgStrPtr fails unless the optional response field is present and equal.
func assertOrgStrPtr(t *testing.T, field string, got *string, want string) {
	t.Helper()
	if got == nil || *got != want {
		t.Errorf("%s: got %v, want %q", field, got, want)
	}
}

// =============================================================================
// UPDATE
// =============================================================================

// TestHandleUpdateOrganisation covers PUT /api/v1/organisation and its
// owner/admin-only authorization plus input validation.
func TestHandleUpdateOrganisation(t *testing.T) {
	ts := newTestServer(t)
	defer ts.pool.Close()

	t.Run("owner updates → 200, round-trips, persists, re-GET reflects", func(t *testing.T) {
		orgB, ownerB := newOrgWithOwner(t, ts)
		authHeader := bearer(t, ts, ownerB, orgB)

		body := UpdateOrganisationRequest{
			Name:                    "AXION LONDON LIMITED",
			LegalName:               ptr("Axion London Ltd"),
			CompanyType:             ptr("limited_company"),
			CompaniesHouseNumber:    ptr("17153114"),
			Utr:                     ptr("1461014737"),
			PayeReference:           ptr("120/RF11544"),
			AccountsOfficeReference: ptr("120PZ03790092"),
			AddressLine1:            ptr("26 Effra Road"),
			Town:                    ptr("London"),
			Region:                  ptr("Greater London"),
			Postcode:                ptr("SW19 8PP"),
			CountryCode:             "gb", // lowercase on purpose — service must upper-case it
			BusinessPhone:           ptr("07340310347"),
			ContactEmail:            ptr("hello@axion.example"),
			ContactPhone:            ptr("020 7946 0000"),
			Website:                 ptr("https://axion.example"),
			BusinessCategory:        ptr("Marketing & Advertising"),
			BusinessDescription:     ptr("A test business."),
		}

		rec := putOrganisation(t, ts, authHeader, body)
		if rec.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d — body: %s", rec.Code, rec.Body.String())
		}
		got := decodeOrganisation(t, rec.Body.Bytes())

		if got.ID != orgB {
			t.Errorf("id: got %q, want %q", got.ID, orgB)
		}
		if got.Name != "AXION LONDON LIMITED" {
			t.Errorf("name: got %q, want %q", got.Name, "AXION LONDON LIMITED")
		}
		if got.CountryCode != "GB" {
			t.Errorf("country_code: got %q, want %q (should be upper-cased)", got.CountryCode, "GB")
		}
		assertOrgStrPtr(t, "company_type", got.CompanyType, "limited_company")
		assertOrgStrPtr(t, "companies_house_number", got.CompaniesHouseNumber, "17153114")
		assertOrgStrPtr(t, "utr", got.Utr, "1461014737")
		assertOrgStrPtr(t, "paye_reference", got.PayeReference, "120/RF11544")
		assertOrgStrPtr(t, "accounts_office_reference", got.AccountsOfficeReference, "120PZ03790092")
		assertOrgStrPtr(t, "address_line_1", got.AddressLine1, "26 Effra Road")
		assertOrgStrPtr(t, "postcode", got.Postcode, "SW19 8PP")
		assertOrgStrPtr(t, "website", got.Website, "https://axion.example")
		assertOrgStrPtr(t, "business_category", got.BusinessCategory, "Marketing & Advertising")

		// Persisted across a fresh read.
		getRec := getOrganisationReq(t, ts, authHeader)
		if getRec.Code != http.StatusOK {
			t.Fatalf("re-GET: expected 200, got %d — body: %s", getRec.Code, getRec.Body.String())
		}
		reread := decodeOrganisation(t, getRec.Body.Bytes())
		if reread.Name != "AXION LONDON LIMITED" {
			t.Errorf("re-GET name: got %q, want %q", reread.Name, "AXION LONDON LIMITED")
		}
		assertOrgStrPtr(t, "re-GET company_type", reread.CompanyType, "limited_company")

		// Row actually committed — only a real DB can prove this.
		var dbType, dbCRN, dbCountry, dbPaye string
		if err := ts.pool.QueryRow(context.Background(),
			"SELECT company_type, companies_house_number, country_code, paye_reference FROM organisations WHERE id = $1",
			orgB).Scan(&dbType, &dbCRN, &dbCountry, &dbPaye); err != nil {
			t.Fatalf("org not found in DB: %v", err)
		}
		if dbType != "limited_company" || dbCRN != "17153114" || dbCountry != "GB" || dbPaye != "120/RF11544" {
			t.Errorf("DB row mismatch: company_type=%q crn=%q country=%q paye=%q", dbType, dbCRN, dbCountry, dbPaye)
		}
	})

	t.Run("non-admin member cannot edit → 403", func(t *testing.T) {
		orgB, _ := newOrgWithOwner(t, ts)
		memberB := newMemberUser(t, ts, orgB)

		rec := putOrganisation(t, ts, bearer(t, ts, memberB, orgB), UpdateOrganisationRequest{Name: "Nope"})
		if rec.Code != http.StatusForbidden {
			t.Errorf("member editing company details: expected 403, got %d — body: %s", rec.Code, rec.Body.String())
		}
	})

	t.Run("non-member cannot edit → 403", func(t *testing.T) {
		orgB, _ := newOrgWithOwner(t, ts)
		// devUserID is a member of the dev org, not org B — a token scoped to org B
		// is rejected by the membership check.
		rec := putOrganisation(t, ts, bearer(t, ts, devUserID, orgB), UpdateOrganisationRequest{Name: "Nope"})
		if rec.Code != http.StatusForbidden {
			t.Errorf("non-member editing: expected 403, got %d — body: %s", rec.Code, rec.Body.String())
		}
	})

	t.Run("missing name → 400 binding", func(t *testing.T) {
		orgB, ownerB := newOrgWithOwner(t, ts)
		rec := putOrganisation(t, ts, bearer(t, ts, ownerB, orgB), UpdateOrganisationRequest{}) // name empty
		if rec.Code != http.StatusBadRequest {
			t.Errorf("expected 400, got %d — body: %s", rec.Code, rec.Body.String())
		}
	})

	t.Run("invalid company_type → 400 binding", func(t *testing.T) {
		orgB, ownerB := newOrgWithOwner(t, ts)
		rec := putOrganisation(t, ts, bearer(t, ts, ownerB, orgB),
			UpdateOrganisationRequest{Name: "X", CompanyType: ptr("corporation")})
		if rec.Code != http.StatusBadRequest {
			t.Errorf("expected 400, got %d — body: %s", rec.Code, rec.Body.String())
		}
	})

	t.Run("invalid country_code → 400 binding", func(t *testing.T) {
		orgB, ownerB := newOrgWithOwner(t, ts)
		rec := putOrganisation(t, ts, bearer(t, ts, ownerB, orgB),
			UpdateOrganisationRequest{Name: "X", CountryCode: "GBR"}) // 3 letters fails len=2
		if rec.Code != http.StatusBadRequest {
			t.Errorf("expected 400, got %d — body: %s", rec.Code, rec.Body.String())
		}
	})

	t.Run("requires auth → 401", func(t *testing.T) {
		rec := putOrganisation(t, ts, "", UpdateOrganisationRequest{Name: "X"})
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("expected 401, got %d — body: %s", rec.Code, rec.Body.String())
		}
	})
}

// TestOrganisationService_Validation_Direct exercises the service-layer guards
// directly (bypassing the handler's `oneof`/`len` bindings) to prove invalid
// input is a validation error (422), independent of the HTTP boundary.
func TestOrganisationService_Validation_Direct(t *testing.T) {
	ts := newTestServer(t)
	defer ts.pool.Close()

	orgB, ownerB := newOrgWithOwner(t, ts)

	cases := []struct {
		name string
		req  UpdateOrganisationRequest
	}{
		{"invalid company_type", UpdateOrganisationRequest{Name: "X", CompanyType: ptr("corporation")}},
		{"invalid country_code", UpdateOrganisationRequest{Name: "X", CountryCode: "ZZZ"}},
		{"blank name", UpdateOrganisationRequest{Name: "   "}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ts.server.organisationService.UpdateOrganisation(
				context.Background(), mustUUID(t, ownerB), mustUUID(t, orgB), tc.req)
			assertAppCode(t, err, ErrCodeValidation)
		})
	}
}

// TestOrganisationService_FieldPreservation proves the read-modify-write keeps the
// columns the Company Details form does NOT edit (slug, native_currency, timezone,
// vrn) while still applying the fields it does — a PUT from this form must not wipe
// them.
func TestOrganisationService_FieldPreservation(t *testing.T) {
	ts := newTestServer(t)
	defer ts.pool.Close()

	orgB, ownerB := newOrgWithOwner(t, ts)

	// Seed the not-on-this-form columns with sentinel values (slug is UNIQUE, so
	// derive it from the org id).
	slug := "pre-slug-" + orgB
	if _, err := ts.pool.Exec(context.Background(),
		`UPDATE organisations
		    SET slug = $2, native_currency = 'USD', timezone = 'America/New_York', vrn = 'GB999999999'
		  WHERE id = $1`, orgB, slug); err != nil {
		t.Fatalf("seed org: %v", err)
	}

	// PUT company details — none of slug/native_currency/timezone/vrn are sent.
	rec := putOrganisation(t, ts, bearer(t, ts, ownerB, orgB),
		UpdateOrganisationRequest{Name: "Preserved Co", CompanyType: ptr("llp"), CountryCode: "GB"})
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d — body: %s", rec.Code, rec.Body.String())
	}

	var gotSlug, gotCur, gotTz, gotVrn, gotName, gotType string
	if err := ts.pool.QueryRow(context.Background(),
		`SELECT slug, native_currency, timezone, vrn, name, company_type FROM organisations WHERE id = $1`,
		orgB).Scan(&gotSlug, &gotCur, &gotTz, &gotVrn, &gotName, &gotType); err != nil {
		t.Fatalf("read org: %v", err)
	}
	// Preserved.
	if gotSlug != slug {
		t.Errorf("slug not preserved: got %q, want %q", gotSlug, slug)
	}
	if gotCur != "USD" {
		t.Errorf("native_currency not preserved: got %q, want USD", gotCur)
	}
	if gotTz != "America/New_York" {
		t.Errorf("timezone not preserved: got %q, want America/New_York", gotTz)
	}
	if gotVrn != "GB999999999" {
		t.Errorf("vrn not preserved: got %q, want GB999999999", gotVrn)
	}
	// Edited.
	if gotName != "Preserved Co" {
		t.Errorf("name not updated: got %q", gotName)
	}
	if gotType != "llp" {
		t.Errorf("company_type not updated: got %q", gotType)
	}
}

// =============================================================================
// GET
// =============================================================================

// TestHandleGetOrganisation covers GET /api/v1/organisation: any active member
// may read; non-members get 403; auth is required.
func TestHandleGetOrganisation(t *testing.T) {
	ts := newTestServer(t)
	defer ts.pool.Close()

	t.Run("active member can view → 200", func(t *testing.T) {
		orgB, _ := newOrgWithOwner(t, ts)
		memberB := newMemberUser(t, ts, orgB)

		rec := getOrganisationReq(t, ts, bearer(t, ts, memberB, orgB))
		if rec.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d — body: %s", rec.Code, rec.Body.String())
		}
		got := decodeOrganisation(t, rec.Body.Bytes())
		if got.ID != orgB {
			t.Errorf("id: got %q, want %q", got.ID, orgB)
		}
		if got.Name == "" {
			t.Error("name: expected a non-empty value")
		}
	})

	t.Run("non-member → 403", func(t *testing.T) {
		orgB, _ := newOrgWithOwner(t, ts)
		rec := getOrganisationReq(t, ts, bearer(t, ts, devUserID, orgB))
		if rec.Code != http.StatusForbidden {
			t.Errorf("non-member GET: expected 403, got %d — body: %s", rec.Code, rec.Body.String())
		}
	})

	t.Run("requires auth → 401", func(t *testing.T) {
		rec := getOrganisationReq(t, ts, "")
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("expected 401, got %d — body: %s", rec.Code, rec.Body.String())
		}
	})
}

// =============================================================================
// MULTI-TENANT ISOLATION
// =============================================================================

// TestOrganisation_TenantIsolation verifies a user who is not a member of an org
// can neither read nor edit it (the membership check is the guard), and that a
// rejected cross-tenant PUT leaves the target org's row untouched.
func TestOrganisation_TenantIsolation(t *testing.T) {
	ts := newTestServer(t)
	defer ts.pool.Close()

	orgB, ownerB := newOrgWithOwner(t, ts)
	if _, err := ts.pool.Exec(context.Background(),
		`UPDATE organisations SET name = 'Org B Name' WHERE id = $1`, orgB); err != nil {
		t.Fatalf("seed org B name: %v", err)
	}

	// devUserID is a member of the dev org, NOT org B.
	outsider := bearer(t, ts, devUserID, orgB)

	if rec := getOrganisationReq(t, ts, outsider); rec.Code != http.StatusForbidden {
		t.Errorf("cross-tenant GET: expected 403, got %d — body: %s", rec.Code, rec.Body.String())
	}
	if rec := putOrganisation(t, ts, outsider, UpdateOrganisationRequest{Name: "Hacked"}); rec.Code != http.StatusForbidden {
		t.Errorf("cross-tenant PUT: expected 403, got %d — body: %s", rec.Code, rec.Body.String())
	}

	// Org B's row is untouched by the rejected PUT.
	var name string
	if err := ts.pool.QueryRow(context.Background(),
		`SELECT name FROM organisations WHERE id = $1`, orgB).Scan(&name); err != nil {
		t.Fatalf("read org B: %v", err)
	}
	if name != "Org B Name" {
		t.Errorf("org B name changed across tenant boundary: got %q, want %q", name, "Org B Name")
	}

	// The org's own owner can still read it.
	if rec := getOrganisationReq(t, ts, bearer(t, ts, ownerB, orgB)); rec.Code != http.StatusOK {
		t.Errorf("owner reading own org: expected 200, got %d — body: %s", rec.Code, rec.Body.String())
	}
}
