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

	bills "github.com/operationfb/accounting-saas/internal/bills"
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
	// A throwaway spending account in the shared Chart of Accounts so a fresh test
	// org can file expenses against it (account_type is NOT NULL on categories).
	if _, err := ts.pool.Exec(context.Background(),
		`INSERT INTO categories (id, organisation_id, nominal_code, name, account_type)
		 VALUES ($1, $2, $3, 'Sundries', 'ADMIN_EXPENSE')`,
		id, orgID, "VR-"+id[:8]); err != nil {
		t.Fatalf("seed category: %v", err)
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

func vatSeedInvoice(t *testing.T, ts *testServer, orgID, userID, contactID, datedOn string, net, salesTax int64, status string) string {
	t.Helper()
	id := uuid.NewString()
	if _, err := ts.pool.Exec(context.Background(),
		`INSERT INTO invoices
		   (id, organisation_id, created_by_user_id, contact_id, dated_on,
		    net_value_minor, sales_tax_value_minor, total_value_minor, status)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)`,
		id, orgID, userID, contactID, datedOn, net, salesTax, net+salesTax, status); err != nil {
		t.Fatalf("seed invoice: %v", err)
	}
	return id
}

// --- cash-basis seed helpers: a CoA category, a bill, and the bank chain ---

func vatSeedCategory(t *testing.T, ts *testServer, orgID string) string {
	t.Helper()
	id := uuid.NewString()
	if _, err := ts.pool.Exec(context.Background(),
		`INSERT INTO categories (id, organisation_id, nominal_code, name, account_type)
		 VALUES ($1, $2, $3, 'Sundries', 'ADMIN_EXPENSE')`,
		id, orgID, "VC-"+id[:8]); err != nil {
		t.Fatalf("seed category: %v", err)
	}
	return id
}

func vatSeedBill(t *testing.T, ts *testServer, orgID, userID, contactID, catID, datedOn string, net, salesTax int64) string {
	t.Helper()
	id := uuid.NewString()
	if _, err := ts.pool.Exec(context.Background(),
		`INSERT INTO bills
		   (id, organisation_id, created_by_user_id, contact_id, category_id, reference, dated_on,
		    net_value_minor, sales_tax_value_minor, total_value_minor)
		 VALUES ($1,$2,$3,$4,$5,'INV-9',$6,$7,$8,$9)`,
		id, orgID, userID, contactID, catID, datedOn, net, salesTax, net+salesTax); err != nil {
		t.Fatalf("seed bill: %v", err)
	}
	return id
}

func vatSeedBankAccount(t *testing.T, ts *testServer, orgID, userID string) string {
	t.Helper()
	id := uuid.NewString()
	if _, err := ts.pool.Exec(context.Background(),
		`INSERT INTO bank_accounts (id, organisation_id, created_by_user_id, name) VALUES ($1,$2,$3,'Test Account')`,
		id, orgID, userID); err != nil {
		t.Fatalf("seed bank account: %v", err)
	}
	return id
}

func vatSeedBankTxn(t *testing.T, ts *testServer, orgID, acctID, datedOn string, amountMinor int64) string {
	t.Helper()
	id := uuid.NewString()
	if _, err := ts.pool.Exec(context.Background(),
		`INSERT INTO bank_transactions (id, organisation_id, bank_account_id, dated_on, amount_minor)
		 VALUES ($1,$2,$3,$4,$5)`,
		id, orgID, acctID, datedOn, amountMinor); err != nil {
		t.Fatalf("seed bank txn: %v", err)
	}
	return id
}

// vatSeedExplanation inserts one bank explanation. paidInvoiceID / paidBillID are
// nil (NULL) or a UUID string; the engine's cash queries JOIN on those FKs.
func vatSeedExplanation(t *testing.T, ts *testServer, orgID, txnID, datedOn, etype string, grossMinor int64, paidInvoiceID, paidBillID interface{}) {
	t.Helper()
	if _, err := ts.pool.Exec(context.Background(),
		`INSERT INTO bank_transaction_explanations
		   (organisation_id, bank_transaction_id, dated_on, type, gross_value_minor, paid_invoice_id, paid_bill_id)
		 VALUES ($1,$2,$3,$4,$5,$6,$7)`,
		orgID, txnID, datedOn, etype, grossMinor, paidInvoiceID, paidBillID); err != nil {
		t.Fatalf("seed explanation: %v", err)
	}
}

func TestHandleGetVatReturn(t *testing.T) {
	t.Parallel()
	ts := newTestServer(t)
	t.Cleanup(func() { ts.pool.Close() })

	orgID, ownerID := newOrgWithOwner(t, ts)
	authHeader := bearer(t, ts, ownerID, orgID)

	// Delete the seeded source rows before newOrgWithOwner's org delete runs (t.Cleanup
	// is LIFO, and this is registered later), so the org row can be removed cleanly.
	t.Cleanup(func() {
		ctx := context.Background()
		_, _ = ts.pool.Exec(ctx, `DELETE FROM invoices WHERE organisation_id = $1`, orgID)
		_, _ = ts.pool.Exec(ctx, `DELETE FROM expenses WHERE organisation_id = $1`, orgID)
		_, _ = ts.pool.Exec(ctx, `DELETE FROM contacts WHERE organisation_id = $1`, orgID)
		_, _ = ts.pool.Exec(ctx, `DELETE FROM categories WHERE organisation_id = $1`, orgID)
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

// TestHandleGetVatReturnCash verifies the CASH basis: invoices/bills are recognised
// via the bank transactions that SETTLE them (apportioned to the amount paid), NOT
// by document date. Using PARTIAL payments makes the cash figures differ from what
// the full documents would give — proving the documents themselves are excluded.
func TestHandleGetVatReturnCash(t *testing.T) {
	t.Parallel()
	ts := newTestServer(t)
	t.Cleanup(func() { ts.pool.Close() })

	orgID, ownerID := newOrgWithOwner(t, ts)
	authHeader := bearer(t, ts, ownerID, orgID)

	t.Cleanup(func() {
		ctx := context.Background()
		_, _ = ts.pool.Exec(ctx, `DELETE FROM bank_transaction_explanations WHERE organisation_id = $1`, orgID)
		_, _ = ts.pool.Exec(ctx, `DELETE FROM bank_transactions WHERE organisation_id = $1`, orgID)
		_, _ = ts.pool.Exec(ctx, `DELETE FROM bank_accounts WHERE organisation_id = $1`, orgID)
		_, _ = ts.pool.Exec(ctx, `DELETE FROM invoices WHERE organisation_id = $1`, orgID)
		_, _ = ts.pool.Exec(ctx, `DELETE FROM bills WHERE organisation_id = $1`, orgID)
		_, _ = ts.pool.Exec(ctx, `DELETE FROM contacts WHERE organisation_id = $1`, orgID)
		_, _ = ts.pool.Exec(ctx, `DELETE FROM categories WHERE organisation_id = $1`, orgID)
	})

	contactID := vatSeedContact(t, ts, orgID, ownerID)
	catID := vatSeedCategory(t, ts, orgID)

	// Register with CASH basis.
	cashBody := registeredBody()
	cashBody.AccountingBasis = strPtr("cash")
	if rec := putVatSettings(t, ts, authHeader, cashBody); rec.Code != http.StatusOK {
		t.Fatalf("register (cash): %d — %s", rec.Code, rec.Body.String())
	}

	acctID := vatSeedBankAccount(t, ts, orgID, ownerID)

	// £1,200 invoice (VAT £200) part-paid £600 by an INVOICE_RECEIPT in-period →
	// half the output VAT (£100) + half the net (£500).
	invID := vatSeedInvoice(t, ts, orgID, ownerID, contactID, "2026-04-10", 100000, 20000, "SENT")
	rcptTxn := vatSeedBankTxn(t, ts, orgID, acctID, "2026-04-15", 60000)
	vatSeedExplanation(t, ts, orgID, rcptTxn, "2026-04-15", "INVOICE_RECEIPT", 60000, invID, nil)

	// £600 bill (VAT £100) part-paid £300 by a BILL_PAYMENT in-period → half the
	// input VAT (£50) + half the net (£250).
	billID := vatSeedBill(t, ts, orgID, ownerID, contactID, catID, "2026-04-12", 50000, 10000)
	payTxn := vatSeedBankTxn(t, ts, orgID, acctID, "2026-04-18", -30000)
	vatSeedExplanation(t, ts, orgID, payTxn, "2026-04-18", "BILL_PAYMENT", -30000, nil, billID)

	rec := getVatReturnReq(t, ts, authHeader, "2026-05-31")
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d — body: %s", rec.Code, rec.Body.String())
	}
	got := decodeVatReturn(t, rec.Body.Bytes())

	if got.AccountingBasis != "cash" {
		t.Errorf("accounting_basis: got %q, want cash", got.AccountingBasis)
	}
	// Apportioned to the PARTIAL payments (not the full £1,200 / £600 documents).
	want := map[string]string{
		"box1": "100.00", "box3": "100.00", "box4": "50.00",
		"box5": "50.00", "box6": "500.00", "box7": "250.00",
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
	if got.IsReclaim || got.NetDue != "50.00" {
		t.Errorf("net_due=%q is_reclaim=%v, want 50.00 / false", got.NetDue, got.IsReclaim)
	}
	if len(got.SalesLines) != 1 || len(got.PurchaseLines) != 1 {
		t.Errorf("lines: got %d sales / %d purchases, want 1 / 1", len(got.SalesLines), len(got.PurchaseLines))
	}
}

// markFiledReq sends POST /api/v1/vat/returns/:periodKey/mark-filed.
func markFiledReq(t *testing.T, ts *testServer, authHeader, periodKey string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodPost, "/api/v1/vat/returns/"+periodKey+"/mark-filed", nil)
	if authHeader != "" {
		req.Header.Set("Authorization", authHeader)
	}
	ts.server.router.ServeHTTP(rec, req)
	return rec
}

// TestVatFiledPeriodLock covers Mark as filed + the filed-period lock: once a return
// is marked filed, a bill dated inside that period can no longer be created, edited or
// deleted (409), while bills in other periods are unaffected.
func TestVatFiledPeriodLock(t *testing.T) {
	t.Parallel()
	ts := newTestServer(t)
	t.Cleanup(func() { ts.pool.Close() })

	orgID, ownerID := newOrgWithOwner(t, ts)
	authHeader := bearer(t, ts, ownerID, orgID)

	t.Cleanup(func() {
		ctx := context.Background()
		_, _ = ts.pool.Exec(ctx, `DELETE FROM vat_returns WHERE organisation_id = $1`, orgID)
		_, _ = ts.pool.Exec(ctx, `DELETE FROM bills WHERE organisation_id = $1`, orgID)
		_, _ = ts.pool.Exec(ctx, `DELETE FROM contacts WHERE organisation_id = $1`, orgID)
		_, _ = ts.pool.Exec(ctx, `DELETE FROM categories WHERE organisation_id = $1`, orgID)
	})

	contactID := vatSeedContact(t, ts, orgID, ownerID)
	catID := vatSeedCategory(t, ts, orgID)

	// Register (quarterly; first period 2026-03-01 .. 2026-05-31).
	if rec := putVatSettings(t, ts, authHeader, registeredBody()); rec.Code != http.StatusOK {
		t.Fatalf("register: %d — %s", rec.Code, rec.Body.String())
	}

	billBody := func(datedOn string) bills.CreateBillRequest {
		return bills.CreateBillRequest{ContactID: contactID, Reference: "INV-1", DatedOn: datedOn, CategoryID: catID, Total: "120.00"}
	}

	// A bill in the May quarter, created BEFORE filing → allowed.
	rec := postBill(t, ts, authHeader, billBody("2026-04-12"))
	if rec.Code != http.StatusCreated {
		t.Fatalf("create bill before filing: expected 201, got %d — %s", rec.Code, rec.Body.String())
	}
	billID := decodeBill(t, rec.Body.Bytes()).ID

	// File the May period.
	if rec := markFiledReq(t, ts, authHeader, "2026-05-31"); rec.Code != http.StatusOK {
		t.Fatalf("mark filed: expected 200, got %d — %s", rec.Code, rec.Body.String())
	}

	t.Run("the period now reads Marked as filed", func(t *testing.T) {
		got := decodeVatReturn(t, getVatReturnReq(t, ts, authHeader, "2026-05-31").Body.Bytes())
		if got.DisplayStatus != "Marked as filed" {
			t.Errorf("display_status: got %q, want %q", got.DisplayStatus, "Marked as filed")
		}
	})

	t.Run("editing the filed-period bill → 409", func(t *testing.T) {
		rec := putBill(t, ts, billID, authHeader, bills.UpdateBillRequest{
			ContactID: contactID, Reference: "INV-1b", DatedOn: "2026-04-12", CategoryID: catID, Total: "150.00",
		})
		if rec.Code != http.StatusConflict {
			t.Errorf("expected 409, got %d — %s", rec.Code, rec.Body.String())
		}
	})

	t.Run("deleting the filed-period bill → 409", func(t *testing.T) {
		if rec := deleteBillReq(t, ts, billID, authHeader); rec.Code != http.StatusConflict {
			t.Errorf("expected 409, got %d — %s", rec.Code, rec.Body.String())
		}
	})

	t.Run("creating a new bill in the filed period → 409", func(t *testing.T) {
		if rec := postBill(t, ts, authHeader, billBody("2026-05-01")); rec.Code != http.StatusConflict {
			t.Errorf("expected 409, got %d — %s", rec.Code, rec.Body.String())
		}
	})

	t.Run("a bill OUTSIDE the filed period is unaffected", func(t *testing.T) {
		// 2026-07-15 is in the next (Jun–Aug) period, which isn't filed.
		if rec := postBill(t, ts, authHeader, billBody("2026-07-15")); rec.Code != http.StatusCreated {
			t.Errorf("expected 201, got %d — %s", rec.Code, rec.Body.String())
		}
	})

	t.Run("mark-filed is owner/admin only", func(t *testing.T) {
		memberID := newMemberUser(t, ts, orgID)
		if rec := markFiledReq(t, ts, bearer(t, ts, memberID, orgID), "2026-08-31"); rec.Code != http.StatusForbidden {
			t.Errorf("member mark-filed: expected 403, got %d — %s", rec.Code, rec.Body.String())
		}
	})
}

// TestFiledReturnShowsSnapshotNotLiveRecompute proves a FILED return renders from the
// stored snapshot, not a live recompute. After filing, we insert a second approved
// expense into the period DIRECTLY (bypassing the service lock) — a live recompute
// would change Box 4, but the snapshot must hold the figure that was filed.
func TestFiledReturnShowsSnapshotNotLiveRecompute(t *testing.T) {
	t.Parallel()
	ts := newTestServer(t)
	t.Cleanup(func() { ts.pool.Close() })

	orgID, ownerID := newOrgWithOwner(t, ts)
	authHeader := bearer(t, ts, ownerID, orgID)

	t.Cleanup(func() {
		c := context.Background()
		_, _ = ts.pool.Exec(c, `DELETE FROM vat_returns WHERE organisation_id=$1`, orgID)
		_, _ = ts.pool.Exec(c, `DELETE FROM expenses WHERE organisation_id=$1`, orgID)
		_, _ = ts.pool.Exec(c, `DELETE FROM categories WHERE organisation_id=$1`, orgID)
	})

	catID := vatSeedExpenseCategory(t, ts, orgID)
	if rec := putVatSettings(t, ts, authHeader, registeredBody()); rec.Code != http.StatusOK {
		t.Fatalf("register: %d — %s", rec.Code, rec.Body.String())
	}

	// One APPROVED expense in the May-26 quarter: £120 incl £20 input VAT.
	vatSeedExpense(t, ts, orgID, ownerID, catID, "2026-04-12", 12000, 2000, "APPROVED", false, false)

	// Pre-file: the LIVE return reflects that single expense.
	before := decodeVatReturn(t, getVatReturnReq(t, ts, authHeader, "2026-05-31").Body.Bytes())
	if before.Box4 != "20.00" {
		t.Fatalf("pre-file Box4 (live): got %q, want 20.00", before.Box4)
	}

	// File the period — this snapshots Box 4 = £20.00.
	if rec := markFiledReq(t, ts, authHeader, "2026-05-31"); rec.Code != http.StatusOK {
		t.Fatalf("mark filed: %d — %s", rec.Code, rec.Body.String())
	}

	// Simulate post-filing drift the lock would normally prevent: insert a SECOND
	// approved expense in the same period straight into the DB (£30 more input VAT).
	// A live recompute would now read Box 4 = £50; the snapshot must stay £20.
	vatSeedExpense(t, ts, orgID, ownerID, catID, "2026-04-20", 18000, 3000, "APPROVED", false, false)

	after := decodeVatReturn(t, getVatReturnReq(t, ts, authHeader, "2026-05-31").Body.Bytes())
	if after.Box4 != "20.00" {
		t.Errorf("filed Box4: got %q, want 20.00 (the snapshot — a live recompute would be 50.00)", after.Box4)
	}
	if after.DisplayStatus != "Marked as filed" {
		t.Errorf("display_status: got %q, want Marked as filed", after.DisplayStatus)
	}
}
