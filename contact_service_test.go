package main

// contact_service_test.go
// =============================================================================
// Integration tests for the contacts module (POST/GET/PUT/DELETE /api/v1/contacts).
//
// Like the rest of the suite these hit a REAL PostgreSQL database via the shared
// newTestServer harness (server_test.go) and skip cleanly when DATABASE_URL is
// unset. Contacts are user data created through the API in-test (no seed file),
// and every created row is hard-deleted in t.Cleanup so the shared dev DB stays
// clean and the user/org FK cleanups don't trip over a referencing contact.
//
// Coverage: happy path + field round-trip, defaults, the 0-vs-NULL payment-terms
// units rule, validation, the creator/admin authorization on update+delete, soft
// delete, and multi-tenant isolation.
// =============================================================================

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"

	contacts "github.com/operationfb/accounting-saas/internal/contacts"
	testutil "github.com/operationfb/accounting-saas/internal/testutil"
)

// ptr returns a pointer to v — convenient for the optional *string/*bool/*int32
// request fields.
func ptr[T any](v T) *T { return &v }

// =============================================================================
// CONTACT TEST HELPERS
// =============================================================================

// postContact sends POST /api/v1/contacts with the given auth header (empty =
// none) and JSON body, returning the recorder.
func postContact(t *testing.T, ts *testServer, authHeader string, body contacts.CreateContactRequest) *httptest.ResponseRecorder {
	t.Helper()
	bodyBytes, _ := json.Marshal(body)
	rec := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodPost, "/api/v1/contacts", bytes.NewReader(bodyBytes))
	req.Header.Set("Content-Type", "application/json")
	if authHeader != "" {
		req.Header.Set("Authorization", authHeader)
	}
	ts.server.router.ServeHTTP(rec, req)
	return rec
}

// putContact sends PUT /api/v1/contacts/:id.
func putContact(t *testing.T, ts *testServer, id, authHeader string, body contacts.UpdateContactRequest) *httptest.ResponseRecorder {
	t.Helper()
	bodyBytes, _ := json.Marshal(body)
	rec := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodPut, "/api/v1/contacts/"+id, bytes.NewReader(bodyBytes))
	req.Header.Set("Content-Type", "application/json")
	if authHeader != "" {
		req.Header.Set("Authorization", authHeader)
	}
	ts.server.router.ServeHTTP(rec, req)
	return rec
}

// getContactReq sends GET /api/v1/contacts/:id.
func getContactReq(t *testing.T, ts *testServer, id, authHeader string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodGet, "/api/v1/contacts/"+id, nil)
	if authHeader != "" {
		req.Header.Set("Authorization", authHeader)
	}
	ts.server.router.ServeHTTP(rec, req)
	return rec
}

// deleteContactReq sends DELETE /api/v1/contacts/:id.
func deleteContactReq(t *testing.T, ts *testServer, id, authHeader string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodDelete, "/api/v1/contacts/"+id, nil)
	if authHeader != "" {
		req.Header.Set("Authorization", authHeader)
	}
	ts.server.router.ServeHTTP(rec, req)
	return rec
}

// decodeContact pulls the { "contact": {...} } envelope into a contacts.ContactResponse.
func decodeContact(t *testing.T, body []byte) contacts.ContactResponse {
	t.Helper()
	var resp struct {
		Contact contacts.ContactResponse `json:"contact"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		t.Fatalf("decode contact: %v — body: %s", err, string(body))
	}
	return resp.Contact
}

// contactIDsFromList decodes { "contacts": [ {id...}, ... ] } into a slice of ids.
func contactIDsFromList(t *testing.T, body []byte) []string {
	t.Helper()
	var resp struct {
		Contacts []contacts.ContactResponse `json:"contacts"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		t.Fatalf("contactIDsFromList: decode: %v", err)
	}
	ids := make([]string, 0, len(resp.Contacts))
	for _, c := range resp.Contacts {
		ids = append(ids, c.ID)
	}
	return ids
}

// createContactAs creates a minimal contact through the API as the given
// user/org and returns its id, registering hard-delete cleanup.
func createContactAs(t *testing.T, ts *testServer, userID, orgID string) string {
	t.Helper()
	orgName := testutil.RandomContactOrgName()
	rec := postContact(t, ts, bearer(t, ts, userID, orgID), contacts.CreateContactRequest{OrganisationName: &orgName})
	if rec.Code != http.StatusCreated {
		t.Fatalf("createContactAs: expected 201, got %d — body: %s", rec.Code, rec.Body.String())
	}
	id := decodeContact(t, rec.Body.Bytes()).ID
	t.Cleanup(func() {
		_, _ = ts.pool.Exec(context.Background(), "DELETE FROM contacts WHERE id = $1", id)
	})
	return id
}

// insertProjectForContact inserts a minimal project row that references the given
// contact, via raw SQL (the projects domain has no create endpoint wired into
// these tests). It registers a hard-delete cleanup — and because t.Cleanup runs
// LIFO, this project is removed before the contact's own cleanup, so the
// contacts FK never blocks teardown. Used to make a contact "in use" so the
// delete-guard and in_use-flag paths can be exercised.
func insertProjectForContact(t *testing.T, ts *testServer, orgID, contactID string) string {
	t.Helper()
	var projectID string
	// Only the NOT NULL columns without a default are supplied; currency defaults
	// to 'GBP' and status to 'active' from the schema.
	if err := ts.pool.QueryRow(context.Background(),
		"INSERT INTO projects (organisation_id, contact_id, name) VALUES ($1, $2, $3) RETURNING id",
		orgID, contactID, "Test Project "+testutil.RandomString(6)).Scan(&projectID); err != nil {
		t.Fatalf("insert project for contact: %v", err)
	}
	t.Cleanup(func() {
		_, _ = ts.pool.Exec(context.Background(), "DELETE FROM projects WHERE id = $1", projectID)
	})
	return projectID
}

// =============================================================================
// CREATE
// =============================================================================

// TestHandleCreateContact covers POST /api/v1/contacts: a full body round-trips
// and persists, defaults are applied, the payment-terms 0-vs-NULL units rule
// holds, bad input is rejected, and auth is required.
func TestHandleCreateContact(t *testing.T) {
	ts := newTestServer(t)
	defer ts.pool.Close()

	t.Run("full body round-trips and persists", func(t *testing.T) {
		orgName := testutil.RandomContactOrgName()
		body := contacts.CreateContactRequest{
			FirstName:               ptr("Ada"),
			LastName:                ptr("Lovelace"),
			OrganisationName:        &orgName,
			Email:                   ptr(testutil.RandomEmail()),
			Telephone:               ptr("020 7946 0000"),
			AddressLine1:            ptr("1 Test Street"),
			Town:                    ptr("London"),
			Postcode:                ptr("EC1A 1BB"),
			CountryCode:             "gb", // lowercase on purpose — service must upper-case it
			DefaultPaymentTermsDays: ptr(int32(14)),
			ChargeVAT:               "NEVER",
			DisplayContactName:      ptr(false),
			VATRegistrationNumber:   ptr("GB123456789"),
			BankSortCode:            ptr("12-34-56"),
			BankAccountNumber:       ptr("12345678"),
		}

		rec := postContact(t, ts, bearer(t, ts, devUserID, devOrgID), body)
		if rec.Code != http.StatusCreated {
			t.Fatalf("expected 201, got %d — body: %s", rec.Code, rec.Body.String())
		}
		got := decodeContact(t, rec.Body.Bytes())
		t.Cleanup(func() { _, _ = ts.pool.Exec(context.Background(), "DELETE FROM contacts WHERE id = $1", got.ID) })

		if got.ID == "" {
			t.Error("id: expected a non-empty UUID")
		}
		if got.OrganisationID != devOrgID {
			t.Errorf("organisation_id: got %q, want %q", got.OrganisationID, devOrgID)
		}
		if got.CreatedByUserID != devUserID {
			t.Errorf("created_by_user_id: got %q, want %q (must come from the token)", got.CreatedByUserID, devUserID)
		}
		if got.OrganisationName == nil || *got.OrganisationName != orgName {
			t.Errorf("organisation_name: got %v, want %q", got.OrganisationName, orgName)
		}
		if got.CountryCode != "GB" {
			t.Errorf("country_code: got %q, want %q (should be upper-cased)", got.CountryCode, "GB")
		}
		if got.ChargeVAT != "NEVER" {
			t.Errorf("charge_vat: got %q, want %q", got.ChargeVAT, "NEVER")
		}
		if got.DisplayContactName != false {
			t.Errorf("display_contact_name: got %v, want false", got.DisplayContactName)
		}
		if got.DefaultPaymentTermsDays == nil || *got.DefaultPaymentTermsDays != 14 {
			t.Errorf("default_payment_terms_days: got %v, want 14", got.DefaultPaymentTermsDays)
		}
		if !got.IsActive {
			t.Error("is_active: expected true by default")
		}

		// Row actually committed — only a real DB can prove this.
		var dbOrgName, dbChargeVat, dbCountry string
		if err := ts.pool.QueryRow(context.Background(),
			"SELECT organisation_name, charge_vat, country_code FROM contacts WHERE id = $1 AND organisation_id = $2",
			got.ID, devOrgID).Scan(&dbOrgName, &dbChargeVat, &dbCountry); err != nil {
			t.Fatalf("contact not found in DB: %v", err)
		}
		if dbOrgName != orgName || dbChargeVat != "NEVER" || dbCountry != "GB" {
			t.Errorf("DB row mismatch: org=%q charge_vat=%q country=%q", dbOrgName, dbChargeVat, dbCountry)
		}
	})

	t.Run("defaults applied when fields omitted", func(t *testing.T) {
		orgName := testutil.RandomContactOrgName()
		rec := postContact(t, ts, bearer(t, ts, devUserID, devOrgID), contacts.CreateContactRequest{OrganisationName: &orgName})
		if rec.Code != http.StatusCreated {
			t.Fatalf("expected 201, got %d — body: %s", rec.Code, rec.Body.String())
		}
		got := decodeContact(t, rec.Body.Bytes())
		t.Cleanup(func() { _, _ = ts.pool.Exec(context.Background(), "DELETE FROM contacts WHERE id = $1", got.ID) })

		if got.ChargeVAT != "SAME_COUNTRY" {
			t.Errorf("charge_vat default: got %q, want SAME_COUNTRY", got.ChargeVAT)
		}
		if got.CountryCode != "GB" {
			t.Errorf("country_code default: got %q, want GB", got.CountryCode)
		}
		if !got.DisplayContactName {
			t.Error("display_contact_name default: got false, want true")
		}
		if got.InvoiceLanguage != "en" {
			t.Errorf("invoice_language default: got %q, want en", got.InvoiceLanguage)
		}
		if got.DefaultPaymentTermsDays != nil {
			t.Errorf("default_payment_terms_days: got %v, want nil when omitted", *got.DefaultPaymentTermsDays)
		}
	})

	t.Run("payment terms 0 persists as 0, omitted persists as NULL", func(t *testing.T) {
		// 0 ("Due on Receipt") must survive as 0 — NOT collapse to NULL.
		orgName := testutil.RandomContactOrgName()
		rec := postContact(t, ts, bearer(t, ts, devUserID, devOrgID), contacts.CreateContactRequest{
			OrganisationName:        &orgName,
			DefaultPaymentTermsDays: ptr(int32(0)),
		})
		if rec.Code != http.StatusCreated {
			t.Fatalf("expected 201, got %d — body: %s", rec.Code, rec.Body.String())
		}
		zeroID := decodeContact(t, rec.Body.Bytes()).ID
		t.Cleanup(func() { _, _ = ts.pool.Exec(context.Background(), "DELETE FROM contacts WHERE id = $1", zeroID) })

		var terms *int32
		if err := ts.pool.QueryRow(context.Background(),
			"SELECT default_payment_terms_days FROM contacts WHERE id = $1", zeroID).Scan(&terms); err != nil {
			t.Fatalf("read terms: %v", err)
		}
		if terms == nil || *terms != 0 {
			t.Errorf("terms with 0 sent: got %v, want a non-NULL 0", terms)
		}

		// Omitted → NULL.
		orgName2 := testutil.RandomContactOrgName()
		rec = postContact(t, ts, bearer(t, ts, devUserID, devOrgID), contacts.CreateContactRequest{OrganisationName: &orgName2})
		nullID := decodeContact(t, rec.Body.Bytes()).ID
		t.Cleanup(func() { _, _ = ts.pool.Exec(context.Background(), "DELETE FROM contacts WHERE id = $1", nullID) })

		var terms2 *int32
		if err := ts.pool.QueryRow(context.Background(),
			"SELECT default_payment_terms_days FROM contacts WHERE id = $1", nullID).Scan(&terms2); err != nil {
			t.Fatalf("read terms2: %v", err)
		}
		if terms2 != nil {
			t.Errorf("terms omitted: got %v, want NULL", *terms2)
		}
	})

	t.Run("invalid charge_vat is rejected (400 binding)", func(t *testing.T) {
		orgName := testutil.RandomContactOrgName()
		rec := postContact(t, ts, bearer(t, ts, devUserID, devOrgID), contacts.CreateContactRequest{
			OrganisationName: &orgName,
			ChargeVAT:        "MAYBE",
		})
		if rec.Code != http.StatusBadRequest {
			t.Errorf("expected 400, got %d — body: %s", rec.Code, rec.Body.String())
		}
	})

	t.Run("invalid email is rejected (400 binding)", func(t *testing.T) {
		orgName := testutil.RandomContactOrgName()
		rec := postContact(t, ts, bearer(t, ts, devUserID, devOrgID), contacts.CreateContactRequest{
			OrganisationName: &orgName,
			Email:            ptr("not-an-email"),
		})
		if rec.Code != http.StatusBadRequest {
			t.Errorf("expected 400, got %d — body: %s", rec.Code, rec.Body.String())
		}
	})

	t.Run("requires auth", func(t *testing.T) {
		orgName := testutil.RandomContactOrgName()
		rec := postContact(t, ts, "", contacts.CreateContactRequest{OrganisationName: &orgName})
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("expected 401, got %d — body: %s", rec.Code, rec.Body.String())
		}
	})
}

// TestContactService_InvalidChargeVAT_Direct exercises the service-layer guard
// directly (bypassing the handler's `oneof` binding) to prove an invalid
// charge_vat is a validation error (422), independent of the HTTP boundary.
func TestContactService_InvalidChargeVAT_Direct(t *testing.T) {
	ts := newTestServer(t)
	defer ts.pool.Close()

	orgName := testutil.RandomContactOrgName()
	_, err := ts.contactService.CreateContact(
		context.Background(), mustUUID(t, devUserID), mustUUID(t, devOrgID),
		contacts.CreateContactRequest{OrganisationName: &orgName, ChargeVAT: "MAYBE"},
	)
	assertAppCode(t, err, ErrCodeValidation)
}

// =============================================================================
// GET / LIST
// =============================================================================

// TestHandleGetContactAndList covers GET /api/v1/contacts/:id and the list.
func TestHandleGetContactAndList(t *testing.T) {
	ts := newTestServer(t)
	defer ts.pool.Close()

	t.Run("get found, then appears in list", func(t *testing.T) {
		id := createContactAs(t, ts, devUserID, devOrgID)

		rec := getContactReq(t, ts, id, bearer(t, ts, devUserID, devOrgID))
		if rec.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d — body: %s", rec.Code, rec.Body.String())
		}
		if got := decodeContact(t, rec.Body.Bytes()); got.ID != id {
			t.Errorf("id: got %q, want %q", got.ID, id)
		}

		listRec := httptest.NewRecorder()
		listReq, _ := http.NewRequest(http.MethodGet, "/api/v1/contacts", nil)
		listReq.Header.Set("Authorization", bearer(t, ts, devUserID, devOrgID))
		ts.server.router.ServeHTTP(listRec, listReq)
		if listRec.Code != http.StatusOK {
			t.Fatalf("list: expected 200, got %d — body: %s", listRec.Code, listRec.Body.String())
		}
		if !contains(contactIDsFromList(t, listRec.Body.Bytes()), id) {
			t.Errorf("list should contain the created contact %s", id)
		}
	})

	t.Run("unknown id → 404", func(t *testing.T) {
		rec := getContactReq(t, ts, uuid.NewString(), bearer(t, ts, devUserID, devOrgID))
		if rec.Code != http.StatusNotFound {
			t.Errorf("expected 404, got %d — body: %s", rec.Code, rec.Body.String())
		}
	})

	t.Run("requires auth", func(t *testing.T) {
		rec := getContactReq(t, ts, uuid.NewString(), "")
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("expected 401, got %d — body: %s", rec.Code, rec.Body.String())
		}
	})
}

// =============================================================================
// UPDATE
// =============================================================================

// TestHandleUpdateContact covers PUT /api/v1/contacts/:id and its authorization.
func TestHandleUpdateContact(t *testing.T) {
	ts := newTestServer(t)
	defer ts.pool.Close()

	t.Run("creator updates own → 200, persisted", func(t *testing.T) {
		id := createContactAs(t, ts, devUserID, devOrgID)

		newName := testutil.RandomContactOrgName()
		rec := putContact(t, ts, id, bearer(t, ts, devUserID, devOrgID), contacts.UpdateContactRequest{
			OrganisationName: &newName,
			ChargeVAT:        "ALWAYS",
			CountryCode:      "FR",
		})
		if rec.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d — body: %s", rec.Code, rec.Body.String())
		}
		got := decodeContact(t, rec.Body.Bytes())
		if got.OrganisationName == nil || *got.OrganisationName != newName {
			t.Errorf("organisation_name: got %v, want %q", got.OrganisationName, newName)
		}
		if got.ChargeVAT != "ALWAYS" || got.CountryCode != "FR" {
			t.Errorf("charge_vat/country: got %q/%q, want ALWAYS/FR", got.ChargeVAT, got.CountryCode)
		}

		var dbName, dbCharge string
		if err := ts.pool.QueryRow(context.Background(),
			"SELECT organisation_name, charge_vat FROM contacts WHERE id = $1", id).Scan(&dbName, &dbCharge); err != nil {
			t.Fatalf("db read: %v", err)
		}
		if dbName != newName || dbCharge != "ALWAYS" {
			t.Errorf("db not updated: name=%q charge_vat=%q", dbName, dbCharge)
		}
	})

	t.Run("org owner/admin updates a member's contact → 200", func(t *testing.T) {
		memberID := newMemberUser(t, ts, devOrgID)
		id := createContactAs(t, ts, memberID, devOrgID)

		newName := testutil.RandomContactOrgName()
		rec := putContact(t, ts, id, bearer(t, ts, devUserID, devOrgID), contacts.UpdateContactRequest{OrganisationName: &newName})
		if rec.Code != http.StatusOK {
			t.Fatalf("admin editing member's contact: expected 200, got %d — body: %s", rec.Code, rec.Body.String())
		}
	})

	t.Run("member cannot update another user's contact → 403", func(t *testing.T) {
		ownerContact := createContactAs(t, ts, devUserID, devOrgID)
		memberID := newMemberUser(t, ts, devOrgID)

		newName := testutil.RandomContactOrgName()
		rec := putContact(t, ts, ownerContact, bearer(t, ts, memberID, devOrgID), contacts.UpdateContactRequest{OrganisationName: &newName})
		if rec.Code != http.StatusForbidden {
			t.Errorf("member editing owner's contact: expected 403, got %d — body: %s", rec.Code, rec.Body.String())
		}
	})

	t.Run("unknown id → 404", func(t *testing.T) {
		newName := testutil.RandomContactOrgName()
		rec := putContact(t, ts, uuid.NewString(), bearer(t, ts, devUserID, devOrgID), contacts.UpdateContactRequest{OrganisationName: &newName})
		if rec.Code != http.StatusNotFound {
			t.Errorf("expected 404, got %d — body: %s", rec.Code, rec.Body.String())
		}
	})

	t.Run("requires auth → 401", func(t *testing.T) {
		newName := testutil.RandomContactOrgName()
		rec := putContact(t, ts, uuid.NewString(), "", contacts.UpdateContactRequest{OrganisationName: &newName})
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("expected 401, got %d — body: %s", rec.Code, rec.Body.String())
		}
	})
}

// =============================================================================
// DELETE
// =============================================================================

// TestHandleDeleteContact covers DELETE /api/v1/contacts/:id: a soft-delete of a
// contact, its authorization, and multi-tenant isolation.
func TestHandleDeleteContact(t *testing.T) {
	ts := newTestServer(t)
	defer ts.pool.Close()

	t.Run("creator deletes own → 204, soft-deleted, then 404 + absent from list", func(t *testing.T) {
		id := createContactAs(t, ts, devUserID, devOrgID)

		rec := deleteContactReq(t, ts, id, bearer(t, ts, devUserID, devOrgID))
		if rec.Code != http.StatusNoContent {
			t.Fatalf("expected 204, got %d — body: %s", rec.Code, rec.Body.String())
		}

		// Soft delete: the row remains with deleted_at set AND is_active cleared
		// (we set both so the lifecycle columns stay coherent).
		var deletedAt *time.Time
		var isActive bool
		if err := ts.pool.QueryRow(context.Background(),
			"SELECT deleted_at, is_active FROM contacts WHERE id = $1", id).Scan(&deletedAt, &isActive); err != nil {
			t.Fatalf("read deleted_at/is_active: %v", err)
		}
		if deletedAt == nil {
			t.Error("expected deleted_at to be set after delete")
		}
		if isActive {
			t.Error("expected is_active to be false after delete")
		}

		// Invisible now: GET → 404, absent from the list.
		if getRec := getContactReq(t, ts, id, bearer(t, ts, devUserID, devOrgID)); getRec.Code != http.StatusNotFound {
			t.Errorf("GET after delete: expected 404, got %d", getRec.Code)
		}
		listRec := httptest.NewRecorder()
		listReq, _ := http.NewRequest(http.MethodGet, "/api/v1/contacts", nil)
		listReq.Header.Set("Authorization", bearer(t, ts, devUserID, devOrgID))
		ts.server.router.ServeHTTP(listRec, listReq)
		if contains(contactIDsFromList(t, listRec.Body.Bytes()), id) {
			t.Error("a deleted contact must not appear in the list")
		}
	})

	t.Run("contact in use by a project → 409, not deleted", func(t *testing.T) {
		id := createContactAs(t, ts, devUserID, devOrgID)
		insertProjectForContact(t, ts, devOrgID, id)

		rec := deleteContactReq(t, ts, id, bearer(t, ts, devUserID, devOrgID))
		if rec.Code != http.StatusConflict {
			t.Fatalf("expected 409, got %d — body: %s", rec.Code, rec.Body.String())
		}

		// The blocked delete must leave the contact intact (still readable).
		if getRec := getContactReq(t, ts, id, bearer(t, ts, devUserID, devOrgID)); getRec.Code != http.StatusOK {
			t.Errorf("a contact whose delete was blocked should survive: GET expected 200, got %d", getRec.Code)
		}
	})

	t.Run("member cannot delete another user's contact → 403", func(t *testing.T) {
		ownerContact := createContactAs(t, ts, devUserID, devOrgID)
		memberID := newMemberUser(t, ts, devOrgID)

		rec := deleteContactReq(t, ts, ownerContact, bearer(t, ts, memberID, devOrgID))
		if rec.Code != http.StatusForbidden {
			t.Errorf("member deleting owner's contact: expected 403, got %d — body: %s", rec.Code, rec.Body.String())
		}
	})

	t.Run("unknown id → 404", func(t *testing.T) {
		rec := deleteContactReq(t, ts, uuid.NewString(), bearer(t, ts, devUserID, devOrgID))
		if rec.Code != http.StatusNotFound {
			t.Errorf("expected 404, got %d — body: %s", rec.Code, rec.Body.String())
		}
	})

	t.Run("requires auth → 401", func(t *testing.T) {
		rec := deleteContactReq(t, ts, uuid.NewString(), "")
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("expected 401, got %d — body: %s", rec.Code, rec.Body.String())
		}
	})

	t.Run("another org cannot delete this org's contact → 404", func(t *testing.T) {
		contactA := createContactAs(t, ts, devUserID, devOrgID)
		orgB, userB := newOrgWithOwner(t, ts)

		rec := deleteContactReq(t, ts, contactA, bearer(t, ts, userB, orgB))
		if rec.Code != http.StatusNotFound {
			t.Errorf("cross-tenant delete: expected 404, got %d — body: %s", rec.Code, rec.Body.String())
		}
	})
}

// =============================================================================
// IN-USE FLAG
// =============================================================================

// TestContactInUseFlag verifies GetContact's derived in_use flag: false when no
// entity references the contact, true once a project does. The frontend uses
// this flag to hide the Delete button on the contact edit page.
func TestContactInUseFlag(t *testing.T) {
	ts := newTestServer(t)
	defer ts.pool.Close()

	id := createContactAs(t, ts, devUserID, devOrgID)

	// No references yet → in_use must be false.
	rec := getContactReq(t, ts, id, bearer(t, ts, devUserID, devOrgID))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET: expected 200, got %d — body: %s", rec.Code, rec.Body.String())
	}
	if decodeContact(t, rec.Body.Bytes()).InUse {
		t.Error("expected in_use=false for a contact with no projects")
	}

	// Once a project references it → in_use must be true.
	insertProjectForContact(t, ts, devOrgID, id)
	rec = getContactReq(t, ts, id, bearer(t, ts, devUserID, devOrgID))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET (in use): expected 200, got %d — body: %s", rec.Code, rec.Body.String())
	}
	if !decodeContact(t, rec.Body.Bytes()).InUse {
		t.Error("expected in_use=true once a project references the contact")
	}
}

// =============================================================================
// MULTI-TENANT ISOLATION
// =============================================================================

// TestContacts_TenantIsolation verifies one org can never read, update, or list
// another org's contact: existence is not revealed across tenants (404), and the
// row never leaks into the other org's list.
func TestContacts_TenantIsolation(t *testing.T) {
	ts := newTestServer(t)
	defer ts.pool.Close()

	// A contact owned by the dev org.
	contactA := createContactAs(t, ts, devUserID, devOrgID)

	// A separate org + owner (cleaned up on exit).
	orgB, userB := newOrgWithOwner(t, ts)
	authB := bearer(t, ts, userB, orgB)

	// GET across tenants → 404.
	if rec := getContactReq(t, ts, contactA, authB); rec.Code != http.StatusNotFound {
		t.Errorf("cross-tenant GET: expected 404, got %d — body: %s", rec.Code, rec.Body.String())
	}

	// PUT across tenants → 404.
	newName := testutil.RandomContactOrgName()
	if rec := putContact(t, ts, contactA, authB, contacts.UpdateContactRequest{OrganisationName: &newName}); rec.Code != http.StatusNotFound {
		t.Errorf("cross-tenant PUT: expected 404, got %d — body: %s", rec.Code, rec.Body.String())
	}

	// org B's list must not contain org A's contact.
	listRec := httptest.NewRecorder()
	listReq, _ := http.NewRequest(http.MethodGet, "/api/v1/contacts", nil)
	listReq.Header.Set("Authorization", authB)
	ts.server.router.ServeHTTP(listRec, listReq)
	if listRec.Code != http.StatusOK {
		t.Fatalf("org B list: expected 200, got %d — body: %s", listRec.Code, listRec.Body.String())
	}
	if contains(contactIDsFromList(t, listRec.Body.Bytes()), contactA) {
		t.Error("org B's contact list must not contain org A's contact")
	}
}
