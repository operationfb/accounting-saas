package main

// bill_service_test.go
// =============================================================================
// Integration tests for the bills module (POST/GET/PUT/DELETE /api/v1/bills +
// the /api/v1/bill-categories picker).
//
// Like the rest of the suite these hit a REAL PostgreSQL database via the shared
// newTestServer harness (server_test.go) and skip cleanly when DATABASE_URL is
// unset. Each bill is created through the API and hard-deleted in t.Cleanup; the
// supplier contact comes from createContactAs (contact_service_test.go) and a
// spending category / VAT rate are read from the seeded dev-org reference data.
//
// VAT mirrors the expenses pattern: a fixed-ratio rate has its VAT extracted from
// the VAT-inclusive total; a non-fixed-ratio ("manual") rate takes vat_amount from
// the client. There is no status lifecycle — a bill is editable/deletable only
// while unpaid (paid_value_minor == 0, which the banking module owns).
//
// Coverage: create happy-path (fixed VAT / no VAT / manual VAT) + persistence,
// validation, get/list, update round-trip, the unpaid edit/delete guard, the
// creator-or-admin authz, multi-tenant isolation, and the category picker filter.
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

	bills "github.com/operationfb/accounting-saas/internal/bills"
)

// =============================================================================
// BILL TEST HELPERS
// =============================================================================

func postBill(t *testing.T, ts *testServer, authHeader string, body bills.CreateBillRequest) *httptest.ResponseRecorder {
	t.Helper()
	return doBillReq(t, ts, http.MethodPost, "/api/v1/bills", authHeader, body)
}

func putBill(t *testing.T, ts *testServer, id, authHeader string, body bills.UpdateBillRequest) *httptest.ResponseRecorder {
	t.Helper()
	return doBillReq(t, ts, http.MethodPut, "/api/v1/bills/"+id, authHeader, body)
}

func getBillReq(t *testing.T, ts *testServer, id, authHeader string) *httptest.ResponseRecorder {
	t.Helper()
	return doBillReq(t, ts, http.MethodGet, "/api/v1/bills/"+id, authHeader, nil)
}

func deleteBillReq(t *testing.T, ts *testServer, id, authHeader string) *httptest.ResponseRecorder {
	t.Helper()
	return doBillReq(t, ts, http.MethodDelete, "/api/v1/bills/"+id, authHeader, nil)
}

func listBillsReq(t *testing.T, ts *testServer, authHeader string) *httptest.ResponseRecorder {
	t.Helper()
	return doBillReq(t, ts, http.MethodGet, "/api/v1/bills", authHeader, nil)
}

func billCategoriesReq(t *testing.T, ts *testServer, authHeader string) *httptest.ResponseRecorder {
	t.Helper()
	return doBillReq(t, ts, http.MethodGet, "/api/v1/bill-categories", authHeader, nil)
}

// doBillReq issues an HTTP request against the in-memory router. body may be nil (no
// payload) or any JSON-marshalable value.
func doBillReq(t *testing.T, ts *testServer, method, path, authHeader string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var reader *bytes.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		reader = bytes.NewReader(b)
	} else {
		reader = bytes.NewReader(nil)
	}
	rec := httptest.NewRecorder()
	req, _ := http.NewRequest(method, path, reader)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if authHeader != "" {
		req.Header.Set("Authorization", authHeader)
	}
	ts.server.router.ServeHTTP(rec, req)
	return rec
}

func decodeBill(t *testing.T, body []byte) bills.BillResponse {
	t.Helper()
	var resp struct {
		Bill bills.BillResponse `json:"bill"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		t.Fatalf("decode bill: %v — body: %s", err, string(body))
	}
	return resp.Bill
}

func billIDsFromList(t *testing.T, body []byte) []string {
	t.Helper()
	var resp struct {
		Bills []bills.BillResponse `json:"bills"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		t.Fatalf("billIDsFromList: decode: %v", err)
	}
	ids := make([]string, 0, len(resp.Bills))
	for _, b := range resp.Bills {
		ids = append(ids, b.ID)
	}
	return ids
}

func cleanupBill(t *testing.T, ts *testServer, id string) {
	t.Helper()
	t.Cleanup(func() { _, _ = ts.pool.Exec(context.Background(), "DELETE FROM bills WHERE id = $1", id) })
}

// spendingCategoryID returns a valid spending-category id for the org (cost of sales
// / admin expense / capital asset) from the seeded CoA.
func spendingCategoryID(t *testing.T, ts *testServer, orgID string) string {
	t.Helper()
	var id string
	err := ts.pool.QueryRow(context.Background(),
		`SELECT id FROM categories
		 WHERE organisation_id = $1 AND is_active AND is_system_managed = FALSE
		   AND account_type IN ('COST_OF_SALES','ADMIN_EXPENSE','CAPITAL_ASSET')
		 ORDER BY account_type, nominal_code LIMIT 1`, orgID).Scan(&id)
	if err != nil {
		t.Skipf("no spending category seeded for org %s: %v", orgID, err)
	}
	return id
}

// nonSpendingCategoryID returns an INCOME category id (a real category, but not a
// valid spending account) for the negative-path validation test.
func nonSpendingCategoryID(t *testing.T, ts *testServer, orgID string) string {
	t.Helper()
	var id string
	err := ts.pool.QueryRow(context.Background(),
		`SELECT id FROM categories
		 WHERE organisation_id = $1 AND is_active AND account_type = 'INCOME'
		 ORDER BY nominal_code LIMIT 1`, orgID).Scan(&id)
	if err != nil {
		t.Skipf("no INCOME category seeded for org %s: %v", orgID, err)
	}
	return id
}

// gbStandardRateID returns the GB Standard Rate (20%, fixed-ratio) vat_rates id.
func gbStandardRateID(t *testing.T, ts *testServer) string {
	t.Helper()
	var id string
	err := ts.pool.QueryRow(context.Background(),
		`SELECT id FROM vat_rates
		 WHERE country_code = 'GB' AND is_fixed_ratio = TRUE AND rate_bps = 2000
		   AND (effective_to IS NULL OR effective_to >= CURRENT_DATE)
		 ORDER BY effective_from DESC LIMIT 1`).Scan(&id)
	if err != nil {
		t.Skipf("no GB 20%% fixed VAT rate seeded: %v", err)
	}
	return id
}

// gbManualRateID returns a GB non-fixed-ratio ("manual") vat_rates id.
func gbManualRateID(t *testing.T, ts *testServer) string {
	t.Helper()
	var id string
	err := ts.pool.QueryRow(context.Background(),
		`SELECT id FROM vat_rates
		 WHERE country_code = 'GB' AND is_fixed_ratio = FALSE
		   AND (effective_to IS NULL OR effective_to >= CURRENT_DATE)
		 ORDER BY effective_from DESC LIMIT 1`).Scan(&id)
	if err != nil {
		t.Skipf("no GB manual VAT rate seeded: %v", err)
	}
	return id
}

// createBillAs creates a minimal bill (no VAT) and returns its id, registering
// hard-delete cleanup.
func createBillAs(t *testing.T, ts *testServer, userID, orgID, contactID, categoryID string) string {
	t.Helper()
	rec := postBill(t, ts, bearer(t, ts, userID, orgID), bills.CreateBillRequest{
		ContactID:  contactID,
		Reference:  "BILL-" + uuid.NewString()[:8],
		DatedOn:    today(),
		CategoryID: categoryID,
		Total:      "100.00",
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("createBillAs: expected 201, got %d — body: %s", rec.Code, rec.Body.String())
	}
	id := decodeBill(t, rec.Body.Bytes()).ID
	cleanupBill(t, ts, id)
	return id
}

// setBillPaid simulates the banking module recording a payment against a bill.
func setBillPaid(t *testing.T, ts *testServer, id string, paidMinor int64) {
	t.Helper()
	if _, err := ts.pool.Exec(context.Background(),
		`UPDATE bills SET paid_value_minor = $2 WHERE id = $1`, id, paidMinor); err != nil {
		t.Fatalf("setBillPaid: %v", err)
	}
}

// =============================================================================
// CREATE
// =============================================================================

func TestCreateBill(t *testing.T) {
	t.Parallel()
	ts := newTestServer(t)
	// Isolate under a throwaway org so this test is parallel-safe (shadows the shared dev seed).
	devOrgID, devUserID := newOrgWithOwner(t, ts)
	auth := bearer(t, ts, devUserID, devOrgID)
	categoryID := spendingCategoryID(t, ts, devOrgID)

	t.Run("fixed-ratio VAT is extracted from the inclusive total", func(t *testing.T) {
		contactID := createContactAs(t, ts, devUserID, devOrgID)
		rate := gbStandardRateID(t, ts)

		rec := postBill(t, ts, auth, bills.CreateBillRequest{
			ContactID:  contactID,
			Reference:  "SUP-" + uuid.NewString()[:8],
			DatedOn:    today(),
			DueOn:      ptr(today()),
			Currency:   "gbp",
			CategoryID: categoryID,
			Total:      "120.00",
			VATRateID:  &rate,
		})
		if rec.Code != http.StatusCreated {
			t.Fatalf("expected 201, got %d — body: %s", rec.Code, rec.Body.String())
		}
		got := decodeBill(t, rec.Body.Bytes())
		cleanupBill(t, ts, got.ID)

		if got.ID == "" || got.OrganisationID != devOrgID || got.CreatedByUserID != devUserID {
			t.Errorf("identity wrong: %+v", got)
		}
		if got.Currency != "GBP" {
			t.Errorf("currency: got %q, want GBP (upper-cased)", got.Currency)
		}
		if got.VATRateID == nil || *got.VATRateID != rate {
			t.Errorf("vat_rate_id: got %v, want %s", got.VATRateID, rate)
		}
		if got.VATRate != "20%" {
			t.Errorf("vat_rate display: got %q, want 20%%", got.VATRate)
		}
		// £120 inclusive @ 20% → VAT £20, net £100.
		assertMoney(t, "net_value", got.NetValue, "100.00")
		assertMoney(t, "sales_tax_value", got.SalesTaxValue, "20.00")
		assertMoney(t, "total_value", got.TotalValue, "120.00")
		assertMoney(t, "paid_value", got.PaidValue, "0.00")
		assertMoney(t, "due_value", got.DueValue, "120.00")
		if got.DisplayStatus != "Unpaid" {
			t.Errorf("display_status: got %q, want Unpaid", got.DisplayStatus)
		}

		// Persistence: the stored minor units match.
		var net, vat, total int64
		if err := ts.pool.QueryRow(context.Background(),
			`SELECT net_value_minor, sales_tax_value_minor, total_value_minor FROM bills WHERE id = $1`,
			got.ID).Scan(&net, &vat, &total); err != nil {
			t.Fatalf("read back: %v", err)
		}
		if net != 10000 || vat != 2000 || total != 12000 {
			t.Errorf("stored minor: net=%d vat=%d total=%d, want 10000/2000/12000", net, vat, total)
		}
	})

	t.Run("no VAT rate selected → zero VAT, net = total", func(t *testing.T) {
		contactID := createContactAs(t, ts, devUserID, devOrgID)
		rec := postBill(t, ts, auth, bills.CreateBillRequest{
			ContactID:  contactID,
			Reference:  "SUP-" + uuid.NewString()[:8],
			DatedOn:    today(),
			CategoryID: categoryID,
			Total:      "120.00",
		})
		if rec.Code != http.StatusCreated {
			t.Fatalf("expected 201, got %d — body: %s", rec.Code, rec.Body.String())
		}
		got := decodeBill(t, rec.Body.Bytes())
		cleanupBill(t, ts, got.ID)
		if got.VATRateID != nil {
			t.Errorf("vat_rate_id: got %v, want nil", got.VATRateID)
		}
		if got.VATRate != "" {
			t.Errorf("vat_rate display: got %q, want empty", got.VATRate)
		}
		assertMoney(t, "sales_tax_value", got.SalesTaxValue, "0.00")
		assertMoney(t, "net_value", got.NetValue, "120.00")
		assertMoney(t, "total_value", got.TotalValue, "120.00")
	})

	t.Run("manual (non-fixed-ratio) rate takes vat_amount from the client", func(t *testing.T) {
		contactID := createContactAs(t, ts, devUserID, devOrgID)
		rate := gbManualRateID(t, ts)
		rec := postBill(t, ts, auth, bills.CreateBillRequest{
			ContactID:  contactID,
			Reference:  "SUP-" + uuid.NewString()[:8],
			DatedOn:    today(),
			CategoryID: categoryID,
			Total:      "120.00",
			VATRateID:  &rate,
			VATAmount:  ptr("13.33"),
		})
		if rec.Code != http.StatusCreated {
			t.Fatalf("expected 201, got %d — body: %s", rec.Code, rec.Body.String())
		}
		got := decodeBill(t, rec.Body.Bytes())
		cleanupBill(t, ts, got.ID)
		// VAT is the supplied amount; net = 120.00 − 13.33 = 106.67.
		assertMoney(t, "sales_tax_value", got.SalesTaxValue, "13.33")
		assertMoney(t, "net_value", got.NetValue, "106.67")
		assertMoney(t, "total_value", got.TotalValue, "120.00")
	})
}

// =============================================================================
// VALIDATION
// =============================================================================

func TestBillValidation(t *testing.T) {
	t.Parallel()
	ts := newTestServer(t)
	// Isolate under a throwaway org so this test is parallel-safe (shadows the shared dev seed).
	devOrgID, devUserID := newOrgWithOwner(t, ts)
	auth := bearer(t, ts, devUserID, devOrgID)
	categoryID := spendingCategoryID(t, ts, devOrgID)
	contactID := createContactAs(t, ts, devUserID, devOrgID)

	base := func() bills.CreateBillRequest {
		return bills.CreateBillRequest{
			ContactID:  contactID,
			Reference:  "SUP-" + uuid.NewString()[:8],
			DatedOn:    today(),
			CategoryID: categoryID,
			Total:      "50.00",
		}
	}

	cases := []struct {
		name   string
		mutate func(*bills.CreateBillRequest)
		want   int
	}{
		{"missing reference → 400 (binding)", func(r *bills.CreateBillRequest) { r.Reference = "" }, http.StatusBadRequest},
		{"whitespace reference → 422 (service)", func(r *bills.CreateBillRequest) { r.Reference = "   " }, http.StatusUnprocessableEntity},
		{"bad contact uuid → 400 (binding)", func(r *bills.CreateBillRequest) { r.ContactID = "nope" }, http.StatusBadRequest},
		{"unknown contact → 422", func(r *bills.CreateBillRequest) { r.ContactID = uuid.NewString() }, http.StatusUnprocessableEntity},
		{"unknown category → 422", func(r *bills.CreateBillRequest) { r.CategoryID = uuid.NewString() }, http.StatusUnprocessableEntity},
		{"bad total → 422", func(r *bills.CreateBillRequest) { r.Total = "abc" }, http.StatusUnprocessableEntity},
		{"missing total → 400 (binding)", func(r *bills.CreateBillRequest) { r.Total = "" }, http.StatusBadRequest},
		{"unknown project → 422", func(r *bills.CreateBillRequest) { r.ProjectID = ptr(uuid.NewString()) }, http.StatusUnprocessableEntity},
		{"bad dated_on → 422", func(r *bills.CreateBillRequest) { r.DatedOn = "31/12/2026" }, http.StatusUnprocessableEntity},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			req := base()
			c.mutate(&req)
			rec := postBill(t, ts, auth, req)
			if rec.Code != c.want {
				t.Errorf("got %d, want %d — body: %s", rec.Code, c.want, rec.Body.String())
			}
		})
	}

	t.Run("non-spending category → 422", func(t *testing.T) {
		req := base()
		req.CategoryID = nonSpendingCategoryID(t, ts, devOrgID)
		rec := postBill(t, ts, auth, req)
		if rec.Code != http.StatusUnprocessableEntity {
			t.Errorf("got %d, want 422 — body: %s", rec.Code, rec.Body.String())
		}
	})

	t.Run("contact from another org → 422", func(t *testing.T) {
		orgB, ownerB := newOrgWithOwner(t, ts)
		foreignContact := createContactAs(t, ts, ownerB, orgB)
		req := base()
		req.ContactID = foreignContact
		rec := postBill(t, ts, auth, req)
		if rec.Code != http.StatusUnprocessableEntity {
			t.Errorf("got %d, want 422 — body: %s", rec.Code, rec.Body.String())
		}
	})

	t.Run("VAT rate from another country → 422", func(t *testing.T) {
		var foreignRate string
		err := ts.pool.QueryRow(context.Background(),
			`SELECT id FROM vat_rates WHERE country_code <> 'GB'
			   AND (effective_to IS NULL OR effective_to >= CURRENT_DATE) LIMIT 1`).Scan(&foreignRate)
		if err != nil {
			t.Skip("no non-GB VAT rate seeded — skipping cross-country check")
		}
		req := base()
		req.VATRateID = &foreignRate
		rec := postBill(t, ts, auth, req)
		if rec.Code != http.StatusUnprocessableEntity {
			t.Errorf("got %d, want 422 — body: %s", rec.Code, rec.Body.String())
		}
	})
}

// =============================================================================
// GET / LIST / UPDATE
// =============================================================================

func TestGetAndListBill(t *testing.T) {
	t.Parallel()
	ts := newTestServer(t)
	// Isolate under a throwaway org so this test is parallel-safe (shadows the shared dev seed).
	devOrgID, devUserID := newOrgWithOwner(t, ts)
	auth := bearer(t, ts, devUserID, devOrgID)
	categoryID := spendingCategoryID(t, ts, devOrgID)
	contactID := createContactAs(t, ts, devUserID, devOrgID)
	id := createBillAs(t, ts, devUserID, devOrgID, contactID, categoryID)

	t.Run("get returns the bill", func(t *testing.T) {
		rec := getBillReq(t, ts, id, auth)
		if rec.Code != http.StatusOK {
			t.Fatalf("got %d, want 200 — body: %s", rec.Code, rec.Body.String())
		}
		if decodeBill(t, rec.Body.Bytes()).ID != id {
			t.Errorf("get returned the wrong bill")
		}
	})

	t.Run("list contains the bill", func(t *testing.T) {
		rec := listBillsReq(t, ts, auth)
		if rec.Code != http.StatusOK {
			t.Fatalf("got %d, want 200", rec.Code)
		}
		if !contains(billIDsFromList(t, rec.Body.Bytes()), id) {
			t.Errorf("list does not contain the created bill %s", id)
		}
	})

	t.Run("get unknown id → 404", func(t *testing.T) {
		rec := getBillReq(t, ts, uuid.NewString(), auth)
		if rec.Code != http.StatusNotFound {
			t.Errorf("got %d, want 404", rec.Code)
		}
	})
}

func TestUpdateBill(t *testing.T) {
	t.Parallel()
	ts := newTestServer(t)
	// Isolate under a throwaway org so this test is parallel-safe (shadows the shared dev seed).
	devOrgID, devUserID := newOrgWithOwner(t, ts)
	auth := bearer(t, ts, devUserID, devOrgID)
	categoryID := spendingCategoryID(t, ts, devOrgID)
	contactID := createContactAs(t, ts, devUserID, devOrgID)

	t.Run("round-trip update", func(t *testing.T) {
		id := createBillAs(t, ts, devUserID, devOrgID, contactID, categoryID)
		newRef := "UPD-" + uuid.NewString()[:8]
		rec := putBill(t, ts, id, auth, bills.UpdateBillRequest{
			ContactID:      contactID,
			Reference:      newRef,
			DatedOn:        today(),
			Comments:       ptr("updated comment"),
			IsHirePurchase: true,
			CategoryID:     categoryID,
			Total:          "200.00",
		})
		if rec.Code != http.StatusOK {
			t.Fatalf("got %d, want 200 — body: %s", rec.Code, rec.Body.String())
		}
		got := decodeBill(t, rec.Body.Bytes())
		if got.Reference == nil || *got.Reference != newRef {
			t.Errorf("reference not updated: %v", got.Reference)
		}
		if !got.IsHirePurchase {
			t.Errorf("is_hire_purchase not updated")
		}
		assertMoney(t, "total_value", got.TotalValue, "200.00")
	})

	t.Run("a paid bill cannot be edited → 409", func(t *testing.T) {
		id := createBillAs(t, ts, devUserID, devOrgID, contactID, categoryID)
		setBillPaid(t, ts, id, 5000)
		rec := putBill(t, ts, id, auth, bills.UpdateBillRequest{
			ContactID:  contactID,
			Reference:  "X-" + uuid.NewString()[:8],
			DatedOn:    today(),
			CategoryID: categoryID,
			Total:      "100.00",
		})
		if rec.Code != http.StatusConflict {
			t.Errorf("got %d, want 409 — body: %s", rec.Code, rec.Body.String())
		}
	})
}

// =============================================================================
// DELETE (unpaid guard)
// =============================================================================

func TestDeleteBill(t *testing.T) {
	t.Parallel()
	ts := newTestServer(t)
	// Isolate under a throwaway org so this test is parallel-safe (shadows the shared dev seed).
	devOrgID, devUserID := newOrgWithOwner(t, ts)
	auth := bearer(t, ts, devUserID, devOrgID)
	categoryID := spendingCategoryID(t, ts, devOrgID)
	contactID := createContactAs(t, ts, devUserID, devOrgID)

	t.Run("unpaid bill is soft-deleted", func(t *testing.T) {
		id := createBillAs(t, ts, devUserID, devOrgID, contactID, categoryID)
		rec := deleteBillReq(t, ts, id, auth)
		if rec.Code != http.StatusNoContent {
			t.Fatalf("got %d, want 204 — body: %s", rec.Code, rec.Body.String())
		}
		// Gone from reads...
		if g := getBillReq(t, ts, id, auth); g.Code != http.StatusNotFound {
			t.Errorf("after delete, get got %d, want 404", g.Code)
		}
		// ...but still present (soft delete).
		var deletedAt *time.Time
		if err := ts.pool.QueryRow(context.Background(),
			`SELECT deleted_at FROM bills WHERE id = $1`, id).Scan(&deletedAt); err != nil {
			t.Fatalf("read back: %v", err)
		}
		if deletedAt == nil {
			t.Errorf("deleted_at not set — expected soft delete")
		}
	})

	t.Run("a paid bill cannot be deleted → 409", func(t *testing.T) {
		id := createBillAs(t, ts, devUserID, devOrgID, contactID, categoryID)
		setBillPaid(t, ts, id, 5000)
		rec := deleteBillReq(t, ts, id, auth)
		if rec.Code != http.StatusConflict {
			t.Errorf("got %d, want 409 — body: %s", rec.Code, rec.Body.String())
		}
	})
}

// =============================================================================
// AUTHORIZATION + MULTI-TENANT ISOLATION
// =============================================================================

func TestBillAuthz(t *testing.T) {
	t.Parallel()
	ts := newTestServer(t)
	// Isolate under a throwaway org so this test is parallel-safe (shadows the shared dev seed).
	devOrgID, devUserID := newOrgWithOwner(t, ts)
	categoryID := spendingCategoryID(t, ts, devOrgID)
	contactID := createContactAs(t, ts, devUserID, devOrgID)

	t.Run("a non-creator non-admin member cannot edit → 403", func(t *testing.T) {
		id := createBillAs(t, ts, devUserID, devOrgID, contactID, categoryID)
		memberID := addMember(t, ts, devOrgID, "member")
		rec := putBill(t, ts, id, bearer(t, ts, memberID, devOrgID), bills.UpdateBillRequest{
			ContactID:  contactID,
			Reference:  "X-" + uuid.NewString()[:8],
			DatedOn:    today(),
			CategoryID: categoryID,
			Total:      "10.00",
		})
		if rec.Code != http.StatusForbidden {
			t.Errorf("got %d, want 403 — body: %s", rec.Code, rec.Body.String())
		}
	})

	t.Run("unauthenticated → 401", func(t *testing.T) {
		rec := listBillsReq(t, ts, "")
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("got %d, want 401", rec.Code)
		}
	})
}

func TestBillIsolation(t *testing.T) {
	t.Parallel()
	ts := newTestServer(t)
	// Isolate under a throwaway org so this test is parallel-safe (shadows the shared dev seed).
	devOrgID, devUserID := newOrgWithOwner(t, ts)
	categoryID := spendingCategoryID(t, ts, devOrgID)
	contactID := createContactAs(t, ts, devUserID, devOrgID)
	id := createBillAs(t, ts, devUserID, devOrgID, contactID, categoryID)

	orgB, ownerB := newOrgWithOwner(t, ts)
	authB := bearer(t, ts, ownerB, orgB)

	t.Run("cross-tenant get → 404", func(t *testing.T) {
		if rec := getBillReq(t, ts, id, authB); rec.Code != http.StatusNotFound {
			t.Errorf("got %d, want 404", rec.Code)
		}
	})

	t.Run("cross-tenant update → 404", func(t *testing.T) {
		rec := putBill(t, ts, id, authB, bills.UpdateBillRequest{
			ContactID:  contactID,
			Reference:  "X-" + uuid.NewString()[:8],
			DatedOn:    today(),
			CategoryID: categoryID,
			Total:      "10.00",
		})
		// Either the missing bill (404) or the foreign contact/category (422) — both
		// prove org B can't touch org A's bill. Assert it never succeeds.
		if rec.Code == http.StatusOK {
			t.Errorf("cross-tenant update unexpectedly succeeded")
		}
	})

	t.Run("org B's list excludes org A's bill", func(t *testing.T) {
		rec := listBillsReq(t, ts, authB)
		if rec.Code != http.StatusOK {
			t.Fatalf("got %d, want 200", rec.Code)
		}
		if contains(billIDsFromList(t, rec.Body.Bytes()), id) {
			t.Errorf("org B's list leaked org A's bill %s", id)
		}
	})
}

// =============================================================================
// CATEGORY PICKER
// =============================================================================

func TestBillCategoriesEndpoint(t *testing.T) {
	t.Parallel()
	ts := newTestServer(t)
	t.Cleanup(func() { ts.pool.Close() })
	// Isolate under a throwaway org so this test is parallel-safe (shadows the shared dev seed).
	devOrgID, devUserID := newOrgWithOwner(t, ts)
	// A fresh org has no Chart of Accounts; seed one spending account so the picker isn't empty.
	spendingCategoryForOrg(t, ts, devOrgID)
	rec := billCategoriesReq(t, ts, bearer(t, ts, devUserID, devOrgID))
	if rec.Code != http.StatusOK {
		t.Fatalf("got %d, want 200 — body: %s", rec.Code, rec.Body.String())
	}
	var resp struct {
		BillCategories []bills.BillCategoryResponse `json:"bill_categories"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp.BillCategories) == 0 {
		t.Fatal("expected at least one spending category")
	}
	allowed := map[string]bool{"COST_OF_SALES": true, "ADMIN_EXPENSE": true, "CAPITAL_ASSET": true}
	for _, c := range resp.BillCategories {
		if !allowed[c.AccountType] {
			t.Errorf("category %s (%s) has non-spending account_type %q", c.NominalCode, c.Name, c.AccountType)
		}
	}
}

// =============================================================================
// SMALL HELPERS
// =============================================================================

func assertMoney(t *testing.T, field, got, want string) {
	t.Helper()
	if got != want {
		t.Errorf("%s: got %q, want %q", field, got, want)
	}
}

// =============================================================================
// OUTSTANDING (the banking Bill Payment picker)
// =============================================================================

func TestListOutstandingBills(t *testing.T) {
	t.Parallel()
	ts := newTestServer(t)
	// Isolate under a throwaway org so this test is parallel-safe (shadows the shared dev seed).
	devOrgID, devUserID := newOrgWithOwner(t, ts)
	auth := bearer(t, ts, devUserID, devOrgID)
	categoryID := spendingCategoryID(t, ts, devOrgID)
	contactID := createContactAs(t, ts, devUserID, devOrgID)

	// createBillAs makes a £100 bill. Vary paid_value via SQL (the banking module's job).
	unpaid := createBillAs(t, ts, devUserID, devOrgID, contactID, categoryID)
	partPaid := createBillAs(t, ts, devUserID, devOrgID, contactID, categoryID)
	setBillPaid(t, ts, partPaid, 4000) // £40 of £100 → still owes £60
	fullyPaid := createBillAs(t, ts, devUserID, devOrgID, contactID, categoryID)
	setBillPaid(t, ts, fullyPaid, 10000) // £100 of £100 → due 0

	rec := doBillReq(t, ts, http.MethodGet, "/api/v1/bills/outstanding", auth, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("got %d, want 200 — body: %s", rec.Code, rec.Body.String())
	}
	ids := billIDsFromList(t, rec.Body.Bytes())
	if !contains(ids, unpaid) {
		t.Errorf("outstanding list missing the unpaid bill %s", unpaid)
	}
	if !contains(ids, partPaid) {
		t.Errorf("outstanding list missing the part-paid bill %s", partPaid)
	}
	if contains(ids, fullyPaid) {
		t.Errorf("outstanding list leaked the fully-paid bill %s", fullyPaid)
	}
}
