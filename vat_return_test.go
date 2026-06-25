package main

// vat_return_test.go
// =============================================================================
// Integration tests for the computed VAT return (GET /api/v1/vat/returns/:periodKey)
// on the accrual basis. Real Postgres via the shared harness; a throwaway org so
// the dev org is untouched. The box MATHS is covered exhaustively by the pure
// internal/vat/calculate_test.go — these tests verify the cross-domain db/vat reads
// and their FILTERS (status, date range, needs_review, soft-delete), the period
// resolution, authz, and multi-tenant isolation. Sources seeded: expenses (input
// VAT) + invoices (output VAT); bills + banking source seeding is a follow-up
// (their routing is already proven by the pure test).
// =============================================================================

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"

	vat "github.com/operationfb/accounting-saas/internal/vat"
)

func getVatReturnReq(t *testing.T, ts *testServer, authHeader, periodKey string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodGet, "/api/v1/vat/returns/"+periodKey, nil)
	if authHeader != "" {
		req.Header.Set("Authorization", authHeader)
	}
	ts.server.router.ServeHTTP(rec, req)
	return rec
}

func decodeVatReturn(t *testing.T, body []byte) vat.VatReturnResponse {
	t.Helper()
	var resp struct {
		VatReturn vat.VatReturnResponse `json:"vat_return"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		t.Fatalf("decode vat_return: %v — body: %s", err, string(body))
	}
	return resp.VatReturn
}

func vatSeedExpenseCategory(t *testing.T, ts *testServer, orgID string) string {
	t.Helper()
	id := uuid.NewString()
	if _, err := ts.pool.Exec(context.Background(),
		`INSERT INTO expense_categories (id, organisation_id, nominal_code, name) VALUES ($1, $2, $3, 'Sundries')`,
		id, orgID, "VR-"+id[:8]); err != nil {
		t.Fatalf("seed expense_category: %v", err)
	}
	return id
}

func vatSeedContact(t *testing.T, ts *testServer, orgID, userID string) string {
	t.Helper()
	id := uuid.NewString()
	if _, err := ts.pool.Exec(context.Background(),
		`INSERT INTO contacts (id, organisation_id, created_by_user_id) VALUES ($1, $2, $3)`,
		id, orgID, userID); err != nil {
		t.Fatalf("seed contact: %v", err)
	}
	return id
}

// vatSeedExpense inserts one expense (amounts POSITIVE, as the app stores them).
// deleted toggles a soft delete; needsReview toggles the capture flag.
func vatSeedExpense(t *testing.T, ts *testServer, orgID, userID, catID, datedOn string, nativeGross, nativeVat int32, status string, needsReview, deleted bool) {
	t.Helper()
	var deletedAt interface{}
	if deleted {
		deletedAt = time.Now()
	}
	if _, err := ts.pool.Exec(context.Background(),
		`INSERT INTO expenses
		   (organisation_id, user_id, created_by_user_id, category_id, dated_on, description,
		    gross_value_minor, native_gross_value_minor, native_vat_value_minor,
		    vat_status, ec_status, status, needs_review, deleted_at)
		 VALUES ($1,$2,$2,$3,$4,'test expense',$5,$5,$6,'TAXABLE','UK_NON_EC',$7,$8,$9)`,
		orgID, userID, catID, datedOn, nativeGross, nativeVat, status, needsReview, deletedAt); err != nil {
		t.Fatalf("seed expense: %v", err)
	}
}

func vatSeedInvoice(t *testing.T, ts *testServer, orgID, userID, contactID, datedOn string, net, salesTax int64, status string) {
	t.Helper()
	if _, err := ts.pool.Exec(context.Background(),
		`INSERT INTO invoices
		   (organisation_id, created_by_user_id, contact_id, dated_on,
		    net_value_minor, sales_tax_value_minor, total_value_minor, status)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8)`,
		orgID, userID, contactID, datedOn, net, salesTax, net+salesTax, status); err != nil {
		t.Fatalf("seed invoice: %v", err)
	}
}

func TestHandleGetVatReturn(t *testing.T) {
	ts := newTestServer(t)
	defer ts.pool.Close()

	orgID, ownerID := newOrgWithOwner(t, ts)
	authHeader := bearer(t, ts, ownerID, orgID)

	// Delete the seeded source rows before newOrgWithOwner's org delete runs (t.Cleanup
	// is LIFO, and this is registered later), so the org row can be removed cleanly.
	t.Cleanup(func() {
		ctx := context.Background()
		_, _ = ts.pool.Exec(ctx, `DELETE FROM invoices WHERE organisation_id = $1`, orgID)
		_, _ = ts.pool.Exec(ctx, `DELETE FROM expenses WHERE organisation_id = $1`, orgID)
		_, _ = ts.pool.Exec(ctx, `DELETE FROM contacts WHERE organisation_id = $1`, orgID)
		_, _ = ts.pool.Exec(ctx, `DELETE FROM expense_categories WHERE organisation_id = $1`, orgID)
	})

	catID := vatSeedExpenseCategory(t, ts, orgID)
	contactID := vatSeedContact(t, ts, orgID, ownerID)

	// Register: effective 2026-03-01, first return ends 2026-05-31, quarterly.
	if rec := putVatSettings(t, ts, authHeader, registeredBody()); rec.Code != http.StatusOK {
		t.Fatalf("register: %d — %s", rec.Code, rec.Body.String())
	}

	// Expenses (input VAT): one INCLUDED + four that must each be EXCLUDED by a filter.
	vatSeedExpense(t, ts, orgID, ownerID, catID, "2026-04-07", 198700, 33117, "APPROVED", false, false) // INCLUDED
	vatSeedExpense(t, ts, orgID, ownerID, catID, "2026-04-08", 6000, 1000, "DRAFT", false, false)       // excluded: status
	vatSeedExpense(t, ts, orgID, ownerID, catID, "2026-06-15", 12000, 2000, "APPROVED", false, false)   // excluded: out of period
	vatSeedExpense(t, ts, orgID, ownerID, catID, "2026-04-09", 18000, 3000, "APPROVED", true, false)    // excluded: needs_review
	vatSeedExpense(t, ts, orgID, ownerID, catID, "2026-04-10", 24000, 4000, "APPROVED", false, true)    // excluded: soft-deleted

	// Invoices (output VAT): one INCLUDED + one EXCLUDED.
	vatSeedInvoice(t, ts, orgID, ownerID, contactID, "2026-04-10", 100000, 20000, "SENT") // INCLUDED
	vatSeedInvoice(t, ts, orgID, ownerID, contactID, "2026-04-11", 50000, 10000, "DRAFT") // excluded: status

	t.Run("computes boxes from only the included rows", func(t *testing.T) {
		rec := getVatReturnReq(t, ts, authHeader, "2026-05-31")
		if rec.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d — body: %s", rec.Code, rec.Body.String())
		}
		got := decodeVatReturn(t, rec.Body.Bytes())

		// Only the one SENT invoice (£200 VAT, £1000 net) and the one APPROVED expense
		// (£331.17 VAT, £1655.83 → £1656 net) count.
		want := map[string]string{
			"box1": "200.00", "box3": "200.00", "box4": "331.17",
			"box5": "-131.17", "box6": "1000.00", "box7": "1656.00",
		}
		got9 := map[string]string{
			"box1": got.Box1, "box3": got.Box3, "box4": got.Box4,
			"box5": got.Box5, "box6": got.Box6, "box7": got.Box7,
		}
		for k, v := range want {
			if got9[k] != v {
				t.Errorf("%s: got %q, want %q", k, got9[k], v)
			}
		}
		if !got.IsReclaim || got.NetDue != "-131.17" {
			t.Errorf("net_due=%q is_reclaim=%v, want -131.17 / true", got.NetDue, got.IsReclaim)
		}
		if len(got.SalesLines) != 1 || len(got.PurchaseLines) != 1 {
			t.Errorf("lines: got %d sales / %d purchases, want 1 / 1", len(got.SalesLines), len(got.PurchaseLines))
		}
		if got.PeriodKey != "2026-05-31" || got.Label != "05 26" {
			t.Errorf("period: key=%q label=%q, want 2026-05-31 / 05 26", got.PeriodKey, got.Label)
		}
	})

	t.Run("plain member may read", func(t *testing.T) {
		memberID := newMemberUser(t, ts, orgID)
		if rec := getVatReturnReq(t, ts, bearer(t, ts, memberID, orgID), "2026-05-31"); rec.Code != http.StatusOK {
			t.Errorf("member GET: expected 200, got %d — body: %s", rec.Code, rec.Body.String())
		}
	})

	t.Run("unknown period → 404", func(t *testing.T) {
		if rec := getVatReturnReq(t, ts, authHeader, "2020-01-31"); rec.Code != http.StatusNotFound {
			t.Errorf("expected 404, got %d — body: %s", rec.Code, rec.Body.String())
		}
	})

	t.Run("unauthenticated → 401", func(t *testing.T) {
		if rec := getVatReturnReq(t, ts, "", "2026-05-31"); rec.Code != http.StatusUnauthorized {
			t.Errorf("expected 401, got %d", rec.Code)
		}
	})

	// A second org with the same settings but no transactions must see an empty
	// return — it can't read org A's expenses/invoices (the org comes from the token).
	t.Run("multi-tenant isolation: another org sees an empty return", func(t *testing.T) {
		orgB, ownerB := newOrgWithOwner(t, ts)
		if rec := putVatSettings(t, ts, bearer(t, ts, ownerB, orgB), registeredBody()); rec.Code != http.StatusOK {
			t.Fatalf("org B register: %d — %s", rec.Code, rec.Body.String())
		}
		got := decodeVatReturn(t, getVatReturnReq(t, ts, bearer(t, ts, ownerB, orgB), "2026-05-31").Body.Bytes())
		if got.Box1 != "0.00" || got.Box4 != "0.00" || len(got.SalesLines) != 0 || len(got.PurchaseLines) != 0 {
			t.Errorf("org B should see an empty return, got Box1=%q Box4=%q lines=%d/%d",
				got.Box1, got.Box4, len(got.SalesLines), len(got.PurchaseLines))
		}
	})
}
