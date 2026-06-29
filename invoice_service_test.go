package main

// invoice_service_test.go
// =============================================================================
// Integration tests for the invoices module (POST/GET/PUT/DELETE
// /api/v1/invoices + the status endpoint + the next-reference auto-numbering).
//
// Like the rest of the suite these hit a REAL PostgreSQL database via the shared
// newTestServer harness (server_test.go) and skip cleanly when DATABASE_URL is
// unset. Each invoice is created through the API in-test and hard-deleted in
// t.Cleanup (which cascades to invoice_items); the contact each invoice needs is
// created via createContactAs (contact_service_test.go), whose cleanup runs LIFO
// AFTER the invoice's, so the FK never blocks teardown.
//
// Reference is REQUIRED, so every create/update body carries one (a unique
// randomRef() unless the case needs a specific value). The org's auto-number
// counter is exercised in a FRESH org so it starts at 1.
//
// Coverage: create happy-path with money/VAT roll-up + persistence, validation
// (incl. the required reference), get/list, update (lines rebuilt, DRAFT-only),
// delete (soft, DRAFT-only), the full status lifecycle, the derived display_status,
// auto-numbering (next-reference + increment-on-use), and multi-tenant isolation.
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
	"github.com/shopspring/decimal"

	invoices "github.com/operationfb/accounting-saas/internal/invoices"
	kernel "github.com/operationfb/accounting-saas/internal/kernel"
)

// =============================================================================
// INVOICE TEST HELPERS
// =============================================================================

// simpleItem builds one line request.
func simpleItem(desc, qty, price, rate string) invoices.InvoiceItemRequest {
	return invoices.InvoiceItemRequest{Description: desc, Quantity: qty, Price: price, SalesTaxRate: rate}
}

// randomRef returns a unique, NON-numeric invoice reference — non-numeric so it
// never advances the org's auto-number counter (keeping the counter tests, which
// run in a fresh org, independent of these).
func randomRef() string { return "T-" + uuid.NewString()[:8] }

// cleanupInvoice registers a hard-delete (cascades to invoice_items) for an invoice
// created inline (createInvoiceAs already registers its own).
func cleanupInvoice(t *testing.T, ts *testServer, id string) {
	t.Helper()
	t.Cleanup(func() { _, _ = ts.pool.Exec(context.Background(), "DELETE FROM invoices WHERE id = $1", id) })
}

func postInvoice(t *testing.T, ts *testServer, authHeader string, body invoices.CreateInvoiceRequest) *httptest.ResponseRecorder {
	t.Helper()
	bodyBytes, _ := json.Marshal(body)
	rec := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodPost, "/api/v1/invoices", bytes.NewReader(bodyBytes))
	req.Header.Set("Content-Type", "application/json")
	if authHeader != "" {
		req.Header.Set("Authorization", authHeader)
	}
	ts.server.router.ServeHTTP(rec, req)
	return rec
}

func putInvoice(t *testing.T, ts *testServer, id, authHeader string, body invoices.UpdateInvoiceRequest) *httptest.ResponseRecorder {
	t.Helper()
	bodyBytes, _ := json.Marshal(body)
	rec := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodPut, "/api/v1/invoices/"+id, bytes.NewReader(bodyBytes))
	req.Header.Set("Content-Type", "application/json")
	if authHeader != "" {
		req.Header.Set("Authorization", authHeader)
	}
	ts.server.router.ServeHTTP(rec, req)
	return rec
}

func getInvoiceReq(t *testing.T, ts *testServer, id, authHeader string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodGet, "/api/v1/invoices/"+id, nil)
	if authHeader != "" {
		req.Header.Set("Authorization", authHeader)
	}
	ts.server.router.ServeHTTP(rec, req)
	return rec
}

func deleteInvoiceReq(t *testing.T, ts *testServer, id, authHeader string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodDelete, "/api/v1/invoices/"+id, nil)
	if authHeader != "" {
		req.Header.Set("Authorization", authHeader)
	}
	ts.server.router.ServeHTTP(rec, req)
	return rec
}

// statusInvoiceReq POSTs an action to /api/v1/invoices/:id/status. Uses a raw map
// so tests can send an invalid action and exercise the `oneof` binding (400).
func statusInvoiceReq(t *testing.T, ts *testServer, id, authHeader, action string) *httptest.ResponseRecorder {
	t.Helper()
	bodyBytes, _ := json.Marshal(map[string]string{"action": action})
	rec := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodPost, "/api/v1/invoices/"+id+"/status", bytes.NewReader(bodyBytes))
	req.Header.Set("Content-Type", "application/json")
	if authHeader != "" {
		req.Header.Set("Authorization", authHeader)
	}
	ts.server.router.ServeHTTP(rec, req)
	return rec
}

func decodeInvoice(t *testing.T, body []byte) invoices.InvoiceResponse {
	t.Helper()
	var resp struct {
		Invoice invoices.InvoiceResponse `json:"invoice"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		t.Fatalf("decode invoice: %v — body: %s", err, string(body))
	}
	return resp.Invoice
}

func invoiceIDsFromList(t *testing.T, body []byte) []string {
	t.Helper()
	var resp struct {
		Invoices []invoices.InvoiceResponse `json:"invoices"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		t.Fatalf("invoiceIDsFromList: decode: %v", err)
	}
	ids := make([]string, 0, len(resp.Invoices))
	for _, in := range resp.Invoices {
		ids = append(ids, in.ID)
	}
	return ids
}

// createInvoiceAs creates a minimal one-line invoice for contactID and returns its
// id, registering hard-delete cleanup (cascades to invoice_items). The reference is
// a unique non-numeric value so it never touches the org's auto-number counter.
func createInvoiceAs(t *testing.T, ts *testServer, userID, orgID, contactID string) string {
	t.Helper()
	rec := postInvoice(t, ts, bearer(t, ts, userID, orgID), invoices.CreateInvoiceRequest{
		ContactID: contactID,
		DatedOn:   time.Now().Format("2006-01-02"),
		Reference: randomRef(),
		Items:     []invoices.InvoiceItemRequest{simpleItem("Consulting", "1", "100.00", "20")},
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("createInvoiceAs: expected 201, got %d — body: %s", rec.Code, rec.Body.String())
	}
	id := decodeInvoice(t, rec.Body.Bytes()).ID
	cleanupInvoice(t, ts, id)
	return id
}

// today / yesterday as YYYY-MM-DD.
func today() string     { return time.Now().Format("2006-01-02") }
func yesterday() string { return time.Now().AddDate(0, 0, -1).Format("2006-01-02") }

// seedRate inserts a global FX rate (HOME per 1 unit of code) for a date and
// hard-deletes it in t.Cleanup, so the shared exchange_rates table stays clean.
func seedRate(t *testing.T, ts *testServer, code, day, rate string) {
	t.Helper()
	d, err := time.Parse("2006-01-02", day)
	if err != nil {
		t.Fatalf("seedRate: bad date %q: %v", day, err)
	}
	if _, err := ts.pool.Exec(context.Background(),
		`INSERT INTO exchange_rates (currency, rate_date, rate, source) VALUES ($1, $2, $3, 'test')
		 ON CONFLICT (currency, rate_date) DO UPDATE SET rate = EXCLUDED.rate`, code, d, rate); err != nil {
		t.Fatalf("seedRate insert: %v", err)
	}
	t.Cleanup(func() {
		_, _ = ts.pool.Exec(context.Background(), "DELETE FROM exchange_rates WHERE currency = $1 AND rate_date = $2", code, d)
	})
}

// TestInvoiceForeignCurrencyAutoFillsRate covers Phase 1's invoice auto-fill: a
// foreign invoice raised WITHOUT an exchange_rate gets one from the stored daily
// rate for its date (and the native_* totals are converted with it); an explicit
// rate still wins; and with no stored rate it's a clean 422, not a 500.
func TestInvoiceForeignCurrencyAutoFillsRate(t *testing.T) {
	ts := newTestServer(t)
	defer ts.pool.Close()
	authHeader := bearer(t, ts, devUserID, devOrgID)

	t.Run("auto-fills from the stored rate for the invoice date", func(t *testing.T) {
		contactID := createContactAs(t, ts, devUserID, devOrgID)
		day := today()
		seedRate(t, ts, "EUR", day, "0.86") // £0.86 per €1

		rec := postInvoice(t, ts, authHeader, invoices.CreateInvoiceRequest{
			ContactID: contactID,
			DatedOn:   day,
			Reference: randomRef(),
			Currency:  "EUR",
			// no ExchangeRate — must be auto-filled from the seeded rate
			Items: []invoices.InvoiceItemRequest{simpleItem("Export work", "1", "100.00", "20")},
		})
		if rec.Code != http.StatusCreated {
			t.Fatalf("expected 201 (auto-filled), got %d — body: %s", rec.Code, rec.Body.String())
		}
		got := decodeInvoice(t, rec.Body.Bytes())
		cleanupInvoice(t, ts, got.ID)

		// €120 total (12000 minor EUR). native = round(12000 × 0.86) = 10320 (£103.20).
		var rate string
		var nativeTotal, total int64
		if err := ts.pool.QueryRow(context.Background(),
			"SELECT exchange_rate, native_total_value_minor, total_value_minor FROM invoices WHERE id = $1",
			got.ID).Scan(&rate, &nativeTotal, &total); err != nil {
			t.Fatalf("read invoice: %v", err)
		}
		if total != 12000 {
			t.Errorf("txn total: got %d, want 12000 (€120)", total)
		}
		if nativeTotal != 10320 {
			t.Errorf("native total: got %d, want 10320 (£103.20 at 0.86)", nativeTotal)
		}
		if d, _ := decimal.NewFromString(rate); !d.Equal(decimal.RequireFromString("0.86")) {
			t.Errorf("stored exchange_rate: got %q, want 0.86", rate)
		}
	})

	t.Run("explicit rate still wins over the stored one", func(t *testing.T) {
		contactID := createContactAs(t, ts, devUserID, devOrgID)
		day := today()
		seedRate(t, ts, "EUR", day, "0.86")

		rec := postInvoice(t, ts, authHeader, invoices.CreateInvoiceRequest{
			ContactID:    contactID,
			DatedOn:      day,
			Reference:    randomRef(),
			Currency:     "EUR",
			ExchangeRate: "0.90", // overrides the stored 0.86
			Items:        []invoices.InvoiceItemRequest{simpleItem("Export work", "1", "100.00", "20")},
		})
		if rec.Code != http.StatusCreated {
			t.Fatalf("expected 201, got %d — body: %s", rec.Code, rec.Body.String())
		}
		got := decodeInvoice(t, rec.Body.Bytes())
		cleanupInvoice(t, ts, got.ID)

		var nativeTotal int64
		if err := ts.pool.QueryRow(context.Background(),
			"SELECT native_total_value_minor FROM invoices WHERE id = $1", got.ID).Scan(&nativeTotal); err != nil {
			t.Fatalf("read invoice: %v", err)
		}
		if nativeTotal != 10800 { // 12000 × 0.90
			t.Errorf("native total: got %d, want 10800 (explicit 0.90)", nativeTotal)
		}
	})

	t.Run("no stored rate → 422 (not 500)", func(t *testing.T) {
		contactID := createContactAs(t, ts, devUserID, devOrgID)
		rec := postInvoice(t, ts, authHeader, invoices.CreateInvoiceRequest{
			ContactID: contactID,
			DatedOn:   "1971-01-04", // no EUR rate seeded on/before this date
			Reference: randomRef(),
			Currency:  "EUR",
			Items:     []invoices.InvoiceItemRequest{simpleItem("Export work", "1", "100.00", "20")},
		})
		if rec.Code != http.StatusUnprocessableEntity {
			t.Fatalf("expected 422 (no rate available), got %d — body: %s", rec.Code, rec.Body.String())
		}
	})
}

// TestInvoiceFXSummary covers the read-only "Currency Gains/Losses" panel: a SENT,
// foreign invoice's detail GET carries an fx_summary that revalues the outstanding
// amount at the booking rate vs today's rate (the unrealised gain/loss); realised is
// "0.00" for now and net == unrealized. A native invoice — and a foreign invoice with
// no current rate — omit the panel.
// assertEq is a tiny field-by-field string check used by the FX-summary assertions.
func assertEq(t *testing.T, field, got, want string) {
	t.Helper()
	if got != want {
		t.Errorf("%s: got %q, want %q", field, got, want)
	}
}

func TestInvoiceFXSummary(t *testing.T) {
	ts := newTestServer(t)
	defer ts.pool.Close()
	authHeader := bearer(t, ts, devUserID, devOrgID)

	t.Run("unrealised loss on an open foreign invoice", func(t *testing.T) {
		contactID := createContactAs(t, ts, devUserID, devOrgID)
		bookingDay := "2026-06-01"      // invoice date, in the past
		seedRate(t, ts, "EUR", bookingDay, "0.86") // booking rate: £0.86 per €1
		seedRate(t, ts, "EUR", today(), "0.80")    // today's rate: £0.80 per €1 (EUR weakened)

		// €100 net + 20% VAT = €120 total (12000 minor EUR). Auto-fills the 0.86 rate.
		rec := postInvoice(t, ts, authHeader, invoices.CreateInvoiceRequest{
			ContactID: contactID,
			DatedOn:   bookingDay,
			Reference: randomRef(),
			Currency:  "EUR",
			Items:     []invoices.InvoiceItemRequest{simpleItem("Export work", "1", "100.00", "20")},
		})
		if rec.Code != http.StatusCreated {
			t.Fatalf("create: expected 201, got %d — body: %s", rec.Code, rec.Body.String())
		}
		id := decodeInvoice(t, rec.Body.Bytes()).ID
		cleanupInvoice(t, ts, id)

		// The panel is SENT-only — a DRAFT carries no fx_summary.
		if draft := decodeInvoice(t, getInvoiceReq(t, ts, id, authHeader).Body.Bytes()); draft.FXSummary != nil {
			t.Fatalf("DRAFT invoice should have no fx_summary, got %+v", draft.FXSummary)
		}
		if r := statusInvoiceReq(t, ts, id, authHeader, "issue"); r.Code != http.StatusOK {
			t.Fatalf("issue: expected 200, got %d — body: %s", r.Code, r.Body.String())
		}

		got := decodeInvoice(t, getInvoiceReq(t, ts, id, authHeader).Body.Bytes())
		fx := got.FXSummary
		if fx == nil {
			t.Fatal("SENT foreign invoice should have an fx_summary, got nil")
		}
		// Outstanding = full €120. At booking 0.86 → £103.20; at today 0.80 → £96.00.
		// Unrealised = 96.00 − 103.20 = −£7.20. Realised deferred ("0.00"); net == unrealised.
		assertEq(t, "currency", fx.Currency, "EUR")
		assertEq(t, "base_currency", fx.BaseCurrency, "GBP")
		assertEq(t, "invoice_date", fx.InvoiceDate, bookingDay)
		assertEq(t, "invoice_value", fx.InvoiceValue, "103.20")
		assertEq(t, "today_value", fx.TodayValue, "96.00")
		assertEq(t, "unrealized", fx.Unrealized, "-7.20")
		assertEq(t, "realized", fx.Realized, "0.00")
		assertEq(t, "net", fx.Net, "-7.20")
		if d, _ := decimal.NewFromString(fx.InvoiceRate); !d.Equal(decimal.RequireFromString("0.86")) {
			t.Errorf("invoice_rate: got %q, want 0.86", fx.InvoiceRate)
		}
	})

	t.Run("native (GBP) invoice has no fx panel", func(t *testing.T) {
		contactID := createContactAs(t, ts, devUserID, devOrgID)
		rec := postInvoice(t, ts, authHeader, invoices.CreateInvoiceRequest{
			ContactID: contactID,
			DatedOn:   today(),
			Reference: randomRef(),
			Currency:  "GBP",
			Items:     []invoices.InvoiceItemRequest{simpleItem("Consulting", "1", "100.00", "20")},
		})
		id := decodeInvoice(t, rec.Body.Bytes()).ID
		cleanupInvoice(t, ts, id)
		if r := statusInvoiceReq(t, ts, id, authHeader, "issue"); r.Code != http.StatusOK {
			t.Fatalf("issue: got %d — body: %s", r.Code, r.Body.String())
		}
		if got := decodeInvoice(t, getInvoiceReq(t, ts, id, authHeader).Body.Bytes()); got.FXSummary != nil {
			t.Errorf("native invoice should have no fx_summary, got %+v", got.FXSummary)
		}
	})

	t.Run("foreign invoice with no current rate omits the panel", func(t *testing.T) {
		contactID := createContactAs(t, ts, devUserID, devOrgID)
		// AED isn't in the ECB/Frankfurter set, so the daily refresh never stores a rate
		// for it. Clear any stray rows so the precondition is deterministic on the shared
		// DB, then raise the invoice with an EXPLICIT rate (so create needs no stored
		// rate). The detail's "today" lookup then finds nothing and omits the panel (not a 500).
		if _, err := ts.pool.Exec(context.Background(), "DELETE FROM exchange_rates WHERE currency = 'AED'"); err != nil {
			t.Fatalf("clear AED rates: %v", err)
		}
		rec := postInvoice(t, ts, authHeader, invoices.CreateInvoiceRequest{
			ContactID:    contactID,
			DatedOn:      today(),
			Reference:    randomRef(),
			Currency:     "AED",
			ExchangeRate: "0.21",
			Items:        []invoices.InvoiceItemRequest{simpleItem("Export work", "1", "100.00", "20")},
		})
		if rec.Code != http.StatusCreated {
			t.Fatalf("create: expected 201, got %d — body: %s", rec.Code, rec.Body.String())
		}
		id := decodeInvoice(t, rec.Body.Bytes()).ID
		cleanupInvoice(t, ts, id)
		if r := statusInvoiceReq(t, ts, id, authHeader, "issue"); r.Code != http.StatusOK {
			t.Fatalf("issue: got %d — body: %s", r.Code, r.Body.String())
		}
		if got := decodeInvoice(t, getInvoiceReq(t, ts, id, authHeader).Body.Bytes()); got.FXSummary != nil {
			t.Errorf("foreign invoice with no current rate should omit fx_summary, got %+v", got.FXSummary)
		}
	})
}

// =============================================================================
// CREATE
// =============================================================================

func TestHandleCreateInvoice(t *testing.T) {
	ts := newTestServer(t)
	defer ts.pool.Close()

	t.Run("two lines: totals + VAT correct and persisted", func(t *testing.T) {
		contactID := createContactAs(t, ts, devUserID, devOrgID)
		ref := "INV-" + uuid.NewString()[:8]
		rec := postInvoice(t, ts, bearer(t, ts, devUserID, devOrgID), invoices.CreateInvoiceRequest{
			ContactID: contactID,
			DatedOn:   today(),
			DueOn:     ptr(today()),
			Reference: ref,
			Currency:  "gbp", // lowercase on purpose — service must upper-case it
			Items: []invoices.InvoiceItemRequest{
				simpleItem("Consulting (10 hrs)", "10", "50.00", "20"),
				simpleItem("Materials", "1", "12.34", "0"),
			},
		})
		if rec.Code != http.StatusCreated {
			t.Fatalf("expected 201, got %d — body: %s", rec.Code, rec.Body.String())
		}
		got := decodeInvoice(t, rec.Body.Bytes())
		cleanupInvoice(t, ts, got.ID)

		if got.ID == "" {
			t.Error("id: expected a non-empty UUID")
		}
		if got.OrganisationID != devOrgID {
			t.Errorf("organisation_id: got %q, want %q", got.OrganisationID, devOrgID)
		}
		if got.CreatedByUserID != devUserID {
			t.Errorf("created_by_user_id: got %q, want %q (from the token)", got.CreatedByUserID, devUserID)
		}
		if got.ContactID != contactID {
			t.Errorf("contact_id: got %q, want %q", got.ContactID, contactID)
		}
		if got.Reference == nil || *got.Reference != ref {
			t.Errorf("reference: got %v, want %q", got.Reference, ref)
		}
		if got.Currency != "GBP" {
			t.Errorf("currency: got %q, want GBP (upper-cased)", got.Currency)
		}
		if got.Status != "DRAFT" || got.DisplayStatus != "Draft" {
			t.Errorf("status/display: got %q/%q, want DRAFT/Draft", got.Status, got.DisplayStatus)
		}
		// Header money: net 512.34, vat 100.00, total 612.34, due 612.34.
		if got.NetValue != "512.34" || got.SalesTaxValue != "100.00" || got.TotalValue != "612.34" {
			t.Errorf("header totals: net=%q vat=%q total=%q, want 512.34/100.00/612.34", got.NetValue, got.SalesTaxValue, got.TotalValue)
		}
		if got.PaidValue != "0.00" || got.DueValue != "612.34" {
			t.Errorf("paid/due: got %q/%q, want 0.00/612.34", got.PaidValue, got.DueValue)
		}
		// Lines, in position order, with derived per-line amounts.
		if len(got.Items) != 2 {
			t.Fatalf("items: got %d, want 2", len(got.Items))
		}
		if got.Items[0].Position != 1 || got.Items[0].NetValue != "500.00" || got.Items[0].SalesTaxValue != "100.00" || got.Items[0].TotalValue != "600.00" {
			t.Errorf("item[0]: %+v, want pos1 net500 vat100 total600", got.Items[0])
		}
		if got.Items[1].Position != 2 || got.Items[1].NetValue != "12.34" || got.Items[1].SalesTaxValue != "0.00" {
			t.Errorf("item[1]: %+v, want pos2 net12.34 vat0", got.Items[1])
		}

		// Committed — only a real DB proves this.
		var total int64
		var status string
		var itemCount int
		if err := ts.pool.QueryRow(context.Background(),
			"SELECT total_value_minor, status FROM invoices WHERE id = $1 AND organisation_id = $2", got.ID, devOrgID).Scan(&total, &status); err != nil {
			t.Fatalf("invoice not in DB: %v", err)
		}
		if total != 61234 || status != "DRAFT" {
			t.Errorf("DB row: total=%d status=%q, want 61234/DRAFT", total, status)
		}
		if err := ts.pool.QueryRow(context.Background(),
			"SELECT count(*) FROM invoice_items WHERE invoice_id = $1", got.ID).Scan(&itemCount); err != nil {
			t.Fatalf("count items: %v", err)
		}
		if itemCount != 2 {
			t.Errorf("invoice_items: got %d, want 2", itemCount)
		}
	})

	t.Run("fractional quantity rounds the line to whole pence", func(t *testing.T) {
		contactID := createContactAs(t, ts, devUserID, devOrgID)
		rec := postInvoice(t, ts, bearer(t, ts, devUserID, devOrgID), invoices.CreateInvoiceRequest{
			ContactID: contactID,
			DatedOn:   today(),
			Reference: randomRef(),
			// 1.005 × £1.00 = £1.005 → rounds half-up to £1.01; VAT 20% of 1.01 = 0.202 → 0.20.
			Items: []invoices.InvoiceItemRequest{simpleItem("Odd qty", "1.005", "1.00", "20")},
		})
		if rec.Code != http.StatusCreated {
			t.Fatalf("expected 201, got %d — body: %s", rec.Code, rec.Body.String())
		}
		got := decodeInvoice(t, rec.Body.Bytes())
		cleanupInvoice(t, ts, got.ID)
		if got.NetValue != "1.01" || got.SalesTaxValue != "0.20" || got.TotalValue != "1.21" {
			t.Errorf("rounding: net=%q vat=%q total=%q, want 1.01/0.20/1.21", got.NetValue, got.SalesTaxValue, got.TotalValue)
		}
	})

	t.Run("no items → zero totals, currency defaults to GBP", func(t *testing.T) {
		contactID := createContactAs(t, ts, devUserID, devOrgID)
		rec := postInvoice(t, ts, bearer(t, ts, devUserID, devOrgID), invoices.CreateInvoiceRequest{
			ContactID: contactID,
			DatedOn:   today(),
			Reference: randomRef(),
		})
		if rec.Code != http.StatusCreated {
			t.Fatalf("expected 201, got %d — body: %s", rec.Code, rec.Body.String())
		}
		got := decodeInvoice(t, rec.Body.Bytes())
		cleanupInvoice(t, ts, got.ID)
		if got.TotalValue != "0.00" || got.Currency != "GBP" {
			t.Errorf("got total=%q currency=%q, want 0.00/GBP", got.TotalValue, got.Currency)
		}
		if len(got.Items) != 0 {
			t.Errorf("items: got %d, want 0", len(got.Items))
		}
	})

	t.Run("missing reference → 400 (binding)", func(t *testing.T) {
		contactID := createContactAs(t, ts, devUserID, devOrgID)
		rec := postInvoice(t, ts, bearer(t, ts, devUserID, devOrgID), invoices.CreateInvoiceRequest{ContactID: contactID, DatedOn: today()})
		if rec.Code != http.StatusBadRequest {
			t.Errorf("expected 400, got %d — body: %s", rec.Code, rec.Body.String())
		}
	})

	t.Run("whitespace-only reference → 422 (service guard)", func(t *testing.T) {
		contactID := createContactAs(t, ts, devUserID, devOrgID)
		rec := postInvoice(t, ts, bearer(t, ts, devUserID, devOrgID), invoices.CreateInvoiceRequest{ContactID: contactID, DatedOn: today(), Reference: "   "})
		if rec.Code != http.StatusUnprocessableEntity {
			t.Errorf("expected 422, got %d — body: %s", rec.Code, rec.Body.String())
		}
	})

	t.Run("missing contact_id → 400 (binding)", func(t *testing.T) {
		rec := postInvoice(t, ts, bearer(t, ts, devUserID, devOrgID), invoices.CreateInvoiceRequest{DatedOn: today(), Reference: randomRef()})
		if rec.Code != http.StatusBadRequest {
			t.Errorf("expected 400, got %d — body: %s", rec.Code, rec.Body.String())
		}
	})

	t.Run("malformed contact_id → 400 (binding uuid)", func(t *testing.T) {
		rec := postInvoice(t, ts, bearer(t, ts, devUserID, devOrgID), invoices.CreateInvoiceRequest{ContactID: "not-a-uuid", DatedOn: today(), Reference: randomRef()})
		if rec.Code != http.StatusBadRequest {
			t.Errorf("expected 400, got %d — body: %s", rec.Code, rec.Body.String())
		}
	})

	t.Run("contact from another org → 422", func(t *testing.T) {
		orgB, userB := newOrgWithOwner(t, ts)
		contactB := createContactAs(t, ts, userB, orgB)
		rec := postInvoice(t, ts, bearer(t, ts, devUserID, devOrgID), invoices.CreateInvoiceRequest{
			ContactID: contactB,
			DatedOn:   today(),
			Reference: randomRef(),
		})
		if rec.Code != http.StatusUnprocessableEntity {
			t.Errorf("cross-org contact: expected 422, got %d — body: %s", rec.Code, rec.Body.String())
		}
	})

	t.Run("bad dated_on → 422", func(t *testing.T) {
		contactID := createContactAs(t, ts, devUserID, devOrgID)
		rec := postInvoice(t, ts, bearer(t, ts, devUserID, devOrgID), invoices.CreateInvoiceRequest{ContactID: contactID, DatedOn: "31/12/2026", Reference: randomRef()})
		if rec.Code != http.StatusUnprocessableEntity {
			t.Errorf("expected 422, got %d — body: %s", rec.Code, rec.Body.String())
		}
	})

	t.Run("bad line price → 422", func(t *testing.T) {
		contactID := createContactAs(t, ts, devUserID, devOrgID)
		rec := postInvoice(t, ts, bearer(t, ts, devUserID, devOrgID), invoices.CreateInvoiceRequest{
			ContactID: contactID,
			DatedOn:   today(),
			Reference: randomRef(),
			Items:     []invoices.InvoiceItemRequest{simpleItem("Bad", "1", "not-a-number", "20")},
		})
		if rec.Code != http.StatusUnprocessableEntity {
			t.Errorf("expected 422, got %d — body: %s", rec.Code, rec.Body.String())
		}
	})

	t.Run("requires auth → 401", func(t *testing.T) {
		rec := postInvoice(t, ts, "", invoices.CreateInvoiceRequest{ContactID: uuid.NewString(), DatedOn: today(), Reference: randomRef()})
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("expected 401, got %d — body: %s", rec.Code, rec.Body.String())
		}
	})
}

// TestInvoiceService_BadContact_Direct exercises the service guard directly
// (bypassing the handler binding): a contact that doesn't belong to the org is a
// 422, independent of the HTTP boundary. A valid reference is supplied so the
// reference guard (which runs first) doesn't mask the contact check.
func TestInvoiceService_BadContact_Direct(t *testing.T) {
	ts := newTestServer(t)
	defer ts.pool.Close()

	_, err := ts.invoiceService.CreateInvoice(
		context.Background(), mustUUID(t, devUserID), mustUUID(t, devOrgID),
		invoices.CreateInvoiceRequest{ContactID: uuid.NewString(), DatedOn: today(), Reference: randomRef()},
	)
	assertAppCode(t, err, kernel.ErrCodeValidation)
}

// =============================================================================
// GET / LIST
// =============================================================================

func TestHandleGetAndListInvoice(t *testing.T) {
	ts := newTestServer(t)
	defer ts.pool.Close()

	t.Run("get found with items, then appears in list", func(t *testing.T) {
		contactID := createContactAs(t, ts, devUserID, devOrgID)
		id := createInvoiceAs(t, ts, devUserID, devOrgID, contactID)

		rec := getInvoiceReq(t, ts, id, bearer(t, ts, devUserID, devOrgID))
		if rec.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d — body: %s", rec.Code, rec.Body.String())
		}
		got := decodeInvoice(t, rec.Body.Bytes())
		if got.ID != id {
			t.Errorf("id: got %q, want %q", got.ID, id)
		}
		if len(got.Items) != 1 {
			t.Errorf("detail items: got %d, want 1", len(got.Items))
		}

		listRec := httptest.NewRecorder()
		listReq, _ := http.NewRequest(http.MethodGet, "/api/v1/invoices", nil)
		listReq.Header.Set("Authorization", bearer(t, ts, devUserID, devOrgID))
		ts.server.router.ServeHTTP(listRec, listReq)
		if listRec.Code != http.StatusOK {
			t.Fatalf("list: expected 200, got %d — body: %s", listRec.Code, listRec.Body.String())
		}
		if !contains(invoiceIDsFromList(t, listRec.Body.Bytes()), id) {
			t.Errorf("list should contain invoice %s", id)
		}
	})

	t.Run("unknown id → 404", func(t *testing.T) {
		rec := getInvoiceReq(t, ts, uuid.NewString(), bearer(t, ts, devUserID, devOrgID))
		if rec.Code != http.StatusNotFound {
			t.Errorf("expected 404, got %d — body: %s", rec.Code, rec.Body.String())
		}
	})

	t.Run("requires auth → 401", func(t *testing.T) {
		rec := getInvoiceReq(t, ts, uuid.NewString(), "")
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("expected 401, got %d — body: %s", rec.Code, rec.Body.String())
		}
	})
}

// =============================================================================
// AUTO-NUMBERING (next-reference + increment-on-use)
// =============================================================================

// TestInvoiceAutoNumber runs in a FRESH org (so the counter starts at 1) and checks
// the next-reference endpoint + the "advance only when the suggested number is
// used" rule, plus the duplicate-reference 409.
func TestInvoiceAutoNumber(t *testing.T) {
	ts := newTestServer(t)
	defer ts.pool.Close()

	orgB, userB := newOrgWithOwner(t, ts)
	authB := bearer(t, ts, userB, orgB)
	contactB := createContactAs(t, ts, userB, orgB)

	nextRef := func() string {
		t.Helper()
		rec := httptest.NewRecorder()
		req, _ := http.NewRequest(http.MethodGet, "/api/v1/invoices/next-reference", nil)
		req.Header.Set("Authorization", authB)
		ts.server.router.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("next-reference: expected 200, got %d — body: %s", rec.Code, rec.Body.String())
		}
		var resp struct {
			Reference string `json:"reference"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
			t.Fatalf("decode next-reference: %v", err)
		}
		return resp.Reference
	}
	createWithRef := func(ref string) *httptest.ResponseRecorder {
		rec := postInvoice(t, ts, authB, invoices.CreateInvoiceRequest{ContactID: contactB, DatedOn: today(), Reference: ref})
		if rec.Code == http.StatusCreated {
			cleanupInvoice(t, ts, decodeInvoice(t, rec.Body.Bytes()).ID)
		}
		return rec
	}

	// Fresh org → counter starts at 1 → "001".
	if got := nextRef(); got != "001" {
		t.Fatalf("first next-reference: got %q, want 001", got)
	}

	// Using the suggested "001" advances the counter to 2.
	if rec := createWithRef("001"); rec.Code != http.StatusCreated {
		t.Fatalf("create 001: expected 201, got %d — body: %s", rec.Code, rec.Body.String())
	}
	if got := nextRef(); got != "002" {
		t.Errorf("after using 001: next-reference got %q, want 002", got)
	}

	// A CUSTOM (out-of-sequence) reference does NOT advance the counter.
	if rec := createWithRef("CUSTOM-1"); rec.Code != http.StatusCreated {
		t.Fatalf("create custom: expected 201, got %d — body: %s", rec.Code, rec.Body.String())
	}
	if got := nextRef(); got != "002" {
		t.Errorf("after a custom reference: next-reference got %q, want 002 (unchanged)", got)
	}

	// Using the suggested "002" advances to 3.
	if rec := createWithRef("002"); rec.Code != http.StatusCreated {
		t.Fatalf("create 002: expected 201, got %d — body: %s", rec.Code, rec.Body.String())
	}
	if got := nextRef(); got != "003" {
		t.Errorf("after using 002: next-reference got %q, want 003", got)
	}

	// A duplicate reference is rejected by the partial unique index → 409.
	if rec := createWithRef("001"); rec.Code != http.StatusConflict {
		t.Errorf("duplicate reference: expected 409, got %d — body: %s", rec.Code, rec.Body.String())
	}
}

// TestInvoiceAutoNumber_SelfHeals reproduces the drift bug: a numeric reference is
// in use but the org's stored counter is BEHIND it (e.g. a reference created
// out-of-band, or one the old increment-on-exact-match logic failed to advance).
// The suggestion must jump to one PAST the highest used number, never re-suggesting
// a taken one.
func TestInvoiceAutoNumber_SelfHeals(t *testing.T) {
	ts := newTestServer(t)
	defer ts.pool.Close()

	orgB, userB := newOrgWithOwner(t, ts)
	authB := bearer(t, ts, userB, orgB)
	contactB := createContactAs(t, ts, userB, orgB)

	// Seed an invoice with reference '005' directly, WITHOUT bumping the counter —
	// the fresh org's counter stays at its default 1, so it has drifted behind '005'.
	var invID string
	if err := ts.pool.QueryRow(context.Background(),
		`INSERT INTO invoices (organisation_id, created_by_user_id, contact_id, dated_on, reference)
		 VALUES ($1, $2, $3, $4, '005') RETURNING id`,
		orgB, userB, contactB, today()).Scan(&invID); err != nil {
		t.Fatalf("seed drifted invoice: %v", err)
	}
	cleanupInvoice(t, ts, invID)

	rec := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodGet, "/api/v1/invoices/next-reference", nil)
	req.Header.Set("Authorization", authB)
	ts.server.router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("next-reference: expected 200, got %d — body: %s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Reference string `json:"reference"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode next-reference: %v", err)
	}
	if resp.Reference != "006" {
		t.Errorf("self-heal: next-reference got %q, want 006 (one past the existing 005)", resp.Reference)
	}
}

// =============================================================================
// UPDATE
// =============================================================================

func TestHandleUpdateInvoice(t *testing.T) {
	ts := newTestServer(t)
	defer ts.pool.Close()

	t.Run("creator updates DRAFT: lines rebuilt, totals recomputed", func(t *testing.T) {
		contactID := createContactAs(t, ts, devUserID, devOrgID)
		id := createInvoiceAs(t, ts, devUserID, devOrgID, contactID) // 1 line, total 120.00

		rec := putInvoice(t, ts, id, bearer(t, ts, devUserID, devOrgID), invoices.UpdateInvoiceRequest{
			ContactID: contactID,
			DatedOn:   today(),
			Reference: randomRef(),
			Items: []invoices.InvoiceItemRequest{
				simpleItem("New line A", "2", "100.00", "20"), // net 200, vat 40
				simpleItem("New line B", "1", "50.00", "0"),   // net 50, vat 0
			},
		})
		if rec.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d — body: %s", rec.Code, rec.Body.String())
		}
		got := decodeInvoice(t, rec.Body.Bytes())
		if got.NetValue != "250.00" || got.SalesTaxValue != "40.00" || got.TotalValue != "290.00" {
			t.Errorf("recomputed totals: net=%q vat=%q total=%q, want 250/40/290", got.NetValue, got.SalesTaxValue, got.TotalValue)
		}
		if len(got.Items) != 2 {
			t.Errorf("items after rebuild: got %d, want 2", len(got.Items))
		}
		var dbItemCount int
		if err := ts.pool.QueryRow(context.Background(),
			"SELECT count(*) FROM invoice_items WHERE invoice_id = $1", id).Scan(&dbItemCount); err != nil {
			t.Fatalf("count items: %v", err)
		}
		if dbItemCount != 2 {
			t.Errorf("DB invoice_items after rebuild: got %d, want 2", dbItemCount)
		}
	})

	t.Run("missing reference → 400 (binding)", func(t *testing.T) {
		contactID := createContactAs(t, ts, devUserID, devOrgID)
		id := createInvoiceAs(t, ts, devUserID, devOrgID, contactID)
		rec := putInvoice(t, ts, id, bearer(t, ts, devUserID, devOrgID), invoices.UpdateInvoiceRequest{
			ContactID: contactID, DatedOn: today(),
		})
		if rec.Code != http.StatusBadRequest {
			t.Errorf("expected 400, got %d — body: %s", rec.Code, rec.Body.String())
		}
	})

	t.Run("editing a non-DRAFT invoice → 409", func(t *testing.T) {
		contactID := createContactAs(t, ts, devUserID, devOrgID)
		id := createInvoiceAs(t, ts, devUserID, devOrgID, contactID)
		if rec := statusInvoiceReq(t, ts, id, bearer(t, ts, devUserID, devOrgID), "issue"); rec.Code != http.StatusOK {
			t.Fatalf("issue setup: expected 200, got %d — body: %s", rec.Code, rec.Body.String())
		}
		rec := putInvoice(t, ts, id, bearer(t, ts, devUserID, devOrgID), invoices.UpdateInvoiceRequest{
			ContactID: contactID, DatedOn: today(), Reference: randomRef(),
			Items: []invoices.InvoiceItemRequest{simpleItem("x", "1", "1.00", "0")},
		})
		if rec.Code != http.StatusConflict {
			t.Errorf("editing SENT invoice: expected 409, got %d — body: %s", rec.Code, rec.Body.String())
		}
	})

	t.Run("member cannot update another user's invoice → 403", func(t *testing.T) {
		contactID := createContactAs(t, ts, devUserID, devOrgID)
		id := createInvoiceAs(t, ts, devUserID, devOrgID, contactID)
		memberID := newMemberUser(t, ts, devOrgID)

		rec := putInvoice(t, ts, id, bearer(t, ts, memberID, devOrgID), invoices.UpdateInvoiceRequest{
			ContactID: contactID, DatedOn: today(), Reference: randomRef(),
			Items: []invoices.InvoiceItemRequest{simpleItem("x", "1", "1.00", "0")},
		})
		if rec.Code != http.StatusForbidden {
			t.Errorf("member editing owner's invoice: expected 403, got %d — body: %s", rec.Code, rec.Body.String())
		}
	})

	t.Run("unknown id → 404", func(t *testing.T) {
		contactID := createContactAs(t, ts, devUserID, devOrgID)
		rec := putInvoice(t, ts, uuid.NewString(), bearer(t, ts, devUserID, devOrgID), invoices.UpdateInvoiceRequest{
			ContactID: contactID, DatedOn: today(), Reference: randomRef(),
		})
		if rec.Code != http.StatusNotFound {
			t.Errorf("expected 404, got %d — body: %s", rec.Code, rec.Body.String())
		}
	})
}

// =============================================================================
// DELETE
// =============================================================================

func TestHandleDeleteInvoice(t *testing.T) {
	ts := newTestServer(t)
	defer ts.pool.Close()

	t.Run("creator deletes DRAFT → 204, soft-deleted, then 404 + absent from list", func(t *testing.T) {
		contactID := createContactAs(t, ts, devUserID, devOrgID)
		id := createInvoiceAs(t, ts, devUserID, devOrgID, contactID)

		rec := deleteInvoiceReq(t, ts, id, bearer(t, ts, devUserID, devOrgID))
		if rec.Code != http.StatusNoContent {
			t.Fatalf("expected 204, got %d — body: %s", rec.Code, rec.Body.String())
		}
		var deletedAt *time.Time
		if err := ts.pool.QueryRow(context.Background(),
			"SELECT deleted_at FROM invoices WHERE id = $1", id).Scan(&deletedAt); err != nil {
			t.Fatalf("read deleted_at: %v", err)
		}
		if deletedAt == nil {
			t.Error("expected deleted_at set after delete")
		}
		if getRec := getInvoiceReq(t, ts, id, bearer(t, ts, devUserID, devOrgID)); getRec.Code != http.StatusNotFound {
			t.Errorf("GET after delete: expected 404, got %d", getRec.Code)
		}
	})

	t.Run("deleting a non-DRAFT invoice → 409, survives", func(t *testing.T) {
		contactID := createContactAs(t, ts, devUserID, devOrgID)
		id := createInvoiceAs(t, ts, devUserID, devOrgID, contactID)
		if rec := statusInvoiceReq(t, ts, id, bearer(t, ts, devUserID, devOrgID), "issue"); rec.Code != http.StatusOK {
			t.Fatalf("issue setup: expected 200, got %d — body: %s", rec.Code, rec.Body.String())
		}
		rec := deleteInvoiceReq(t, ts, id, bearer(t, ts, devUserID, devOrgID))
		if rec.Code != http.StatusConflict {
			t.Fatalf("deleting SENT invoice: expected 409, got %d — body: %s", rec.Code, rec.Body.String())
		}
		if getRec := getInvoiceReq(t, ts, id, bearer(t, ts, devUserID, devOrgID)); getRec.Code != http.StatusOK {
			t.Errorf("a blocked delete should leave the invoice readable: GET expected 200, got %d", getRec.Code)
		}
	})

	t.Run("another org cannot delete this org's invoice → 404", func(t *testing.T) {
		contactID := createContactAs(t, ts, devUserID, devOrgID)
		id := createInvoiceAs(t, ts, devUserID, devOrgID, contactID)
		orgB, userB := newOrgWithOwner(t, ts)

		rec := deleteInvoiceReq(t, ts, id, bearer(t, ts, userB, orgB))
		if rec.Code != http.StatusNotFound {
			t.Errorf("cross-tenant delete: expected 404, got %d — body: %s", rec.Code, rec.Body.String())
		}
	})
}

// =============================================================================
// STATUS LIFECYCLE
// =============================================================================

func TestHandleInvoiceStatus(t *testing.T) {
	ts := newTestServer(t)
	defer ts.pool.Close()

	t.Run("issue DRAFT→SENT (display Open), schedule/send, write_off, refund, reopen", func(t *testing.T) {
		contactID := createContactAs(t, ts, devUserID, devOrgID)
		auth := bearer(t, ts, devUserID, devOrgID)

		// issue: DRAFT → SENT, display Open (no due date set → not overdue).
		id := createInvoiceAs(t, ts, devUserID, devOrgID, contactID)
		rec := statusInvoiceReq(t, ts, id, auth, "issue")
		if rec.Code != http.StatusOK {
			t.Fatalf("issue: expected 200, got %d — body: %s", rec.Code, rec.Body.String())
		}
		if got := decodeInvoice(t, rec.Body.Bytes()); got.Status != "SENT" || got.DisplayStatus != "Open" {
			t.Errorf("after issue: status=%q display=%q, want SENT/Open", got.Status, got.DisplayStatus)
		}

		// reopen: SENT → DRAFT.
		if rec := statusInvoiceReq(t, ts, id, auth, "reopen"); rec.Code != http.StatusOK || decodeInvoice(t, rec.Body.Bytes()).Status != "DRAFT" {
			t.Errorf("reopen: expected 200/DRAFT, got %d — body: %s", rec.Code, rec.Body.String())
		}

		// schedule then send.
		if rec := statusInvoiceReq(t, ts, id, auth, "schedule"); rec.Code != http.StatusOK || decodeInvoice(t, rec.Body.Bytes()).Status != "SCHEDULED" {
			t.Errorf("schedule: expected 200/SCHEDULED, got %d — body: %s", rec.Code, rec.Body.String())
		}
		if rec := statusInvoiceReq(t, ts, id, auth, "send"); rec.Code != http.StatusOK || decodeInvoice(t, rec.Body.Bytes()).Status != "SENT" {
			t.Errorf("send: expected 200/SENT, got %d — body: %s", rec.Code, rec.Body.String())
		}

		// write_off: SENT → WRITTEN_OFF.
		if rec := statusInvoiceReq(t, ts, id, auth, "write_off"); rec.Code != http.StatusOK || decodeInvoice(t, rec.Body.Bytes()).Status != "WRITTEN_OFF" {
			t.Errorf("write_off: expected 200/WRITTEN_OFF, got %d — body: %s", rec.Code, rec.Body.String())
		}
	})

	t.Run("illegal transition (refund on DRAFT) → 409", func(t *testing.T) {
		contactID := createContactAs(t, ts, devUserID, devOrgID)
		id := createInvoiceAs(t, ts, devUserID, devOrgID, contactID)
		rec := statusInvoiceReq(t, ts, id, bearer(t, ts, devUserID, devOrgID), "refund")
		if rec.Code != http.StatusConflict {
			t.Errorf("refund on DRAFT: expected 409, got %d — body: %s", rec.Code, rec.Body.String())
		}
	})

	t.Run("unknown action → 400 (binding)", func(t *testing.T) {
		contactID := createContactAs(t, ts, devUserID, devOrgID)
		id := createInvoiceAs(t, ts, devUserID, devOrgID, contactID)
		rec := statusInvoiceReq(t, ts, id, bearer(t, ts, devUserID, devOrgID), "frobnicate")
		if rec.Code != http.StatusBadRequest {
			t.Errorf("bad action: expected 400, got %d — body: %s", rec.Code, rec.Body.String())
		}
	})

	t.Run("cross-tenant status change → 404", func(t *testing.T) {
		contactID := createContactAs(t, ts, devUserID, devOrgID)
		id := createInvoiceAs(t, ts, devUserID, devOrgID, contactID)
		orgB, userB := newOrgWithOwner(t, ts)
		rec := statusInvoiceReq(t, ts, id, bearer(t, ts, userB, orgB), "issue")
		if rec.Code != http.StatusNotFound {
			t.Errorf("cross-tenant status: expected 404, got %d — body: %s", rec.Code, rec.Body.String())
		}
	})
}

// =============================================================================
// DERIVED DISPLAY STATUS
// =============================================================================

func TestInvoiceDisplayStatus(t *testing.T) {
	ts := newTestServer(t)
	defer ts.pool.Close()
	auth := bearer(t, ts, devUserID, devOrgID)

	// setPaid sets paid_value_minor directly (there is no payments API yet).
	setPaid := func(id string, paidMinor int64) {
		t.Helper()
		if _, err := ts.pool.Exec(context.Background(),
			"UPDATE invoices SET paid_value_minor = $1 WHERE id = $2", paidMinor, id); err != nil {
			t.Fatalf("setPaid: %v", err)
		}
	}

	t.Run("SENT with no lines → Zero Value", func(t *testing.T) {
		contactID := createContactAs(t, ts, devUserID, devOrgID)
		rec := postInvoice(t, ts, auth, invoices.CreateInvoiceRequest{ContactID: contactID, DatedOn: today(), Reference: randomRef()})
		if rec.Code != http.StatusCreated {
			t.Fatalf("create: %d — %s", rec.Code, rec.Body.String())
		}
		id := decodeInvoice(t, rec.Body.Bytes()).ID
		cleanupInvoice(t, ts, id)
		if rec := statusInvoiceReq(t, ts, id, auth, "issue"); rec.Code != http.StatusOK {
			t.Fatalf("issue: %d — %s", rec.Code, rec.Body.String())
		}
		if got := decodeInvoice(t, getInvoiceReq(t, ts, id, auth).Body.Bytes()); got.DisplayStatus != "Zero Value" {
			t.Errorf("display: got %q, want Zero Value", got.DisplayStatus)
		}
	})

	t.Run("SENT and fully paid → Paid; overpaid → Overpaid", func(t *testing.T) {
		contactID := createContactAs(t, ts, devUserID, devOrgID)
		id := createInvoiceAs(t, ts, devUserID, devOrgID, contactID) // total 120.00 → 12000
		if rec := statusInvoiceReq(t, ts, id, auth, "issue"); rec.Code != http.StatusOK {
			t.Fatalf("issue: %d — %s", rec.Code, rec.Body.String())
		}
		setPaid(id, 12000)
		if got := decodeInvoice(t, getInvoiceReq(t, ts, id, auth).Body.Bytes()); got.DisplayStatus != "Paid" || got.DueValue != "0.00" {
			t.Errorf("paid: display=%q due=%q, want Paid/0.00", got.DisplayStatus, got.DueValue)
		}
		setPaid(id, 13000)
		if got := decodeInvoice(t, getInvoiceReq(t, ts, id, auth).Body.Bytes()); got.DisplayStatus != "Overpaid" {
			t.Errorf("overpaid: display=%q, want Overpaid", got.DisplayStatus)
		}
	})

	t.Run("SENT and past due → Overdue", func(t *testing.T) {
		contactID := createContactAs(t, ts, devUserID, devOrgID)
		rec := postInvoice(t, ts, auth, invoices.CreateInvoiceRequest{
			ContactID: contactID,
			DatedOn:   yesterday(),
			DueOn:     ptr(yesterday()),
			Reference: randomRef(),
			Items:     []invoices.InvoiceItemRequest{simpleItem("Late", "1", "100.00", "0")},
		})
		if rec.Code != http.StatusCreated {
			t.Fatalf("create: %d — %s", rec.Code, rec.Body.String())
		}
		id := decodeInvoice(t, rec.Body.Bytes()).ID
		cleanupInvoice(t, ts, id)
		if rec := statusInvoiceReq(t, ts, id, auth, "issue"); rec.Code != http.StatusOK {
			t.Fatalf("issue: %d — %s", rec.Code, rec.Body.String())
		}
		if got := decodeInvoice(t, getInvoiceReq(t, ts, id, auth).Body.Bytes()); got.DisplayStatus != "Overdue" {
			t.Errorf("overdue: got %q, want Overdue", got.DisplayStatus)
		}
	})
}

// =============================================================================
// MULTI-TENANT ISOLATION
// =============================================================================

func TestInvoices_TenantIsolation(t *testing.T) {
	ts := newTestServer(t)
	defer ts.pool.Close()

	contactID := createContactAs(t, ts, devUserID, devOrgID)
	invoiceA := createInvoiceAs(t, ts, devUserID, devOrgID, contactID)

	orgB, userB := newOrgWithOwner(t, ts)
	authB := bearer(t, ts, userB, orgB)
	// org B's OWN contact, so the PUT body is valid for org B and the request
	// reaches (and is refused by) the invoice tenant scope — a 404 — rather than
	// tripping the cross-org contact guard (422) first.
	contactB := createContactAs(t, ts, userB, orgB)

	if rec := getInvoiceReq(t, ts, invoiceA, authB); rec.Code != http.StatusNotFound {
		t.Errorf("cross-tenant GET: expected 404, got %d", rec.Code)
	}
	if rec := putInvoice(t, ts, invoiceA, authB, invoices.UpdateInvoiceRequest{ContactID: contactB, DatedOn: today(), Reference: randomRef()}); rec.Code != http.StatusNotFound {
		t.Errorf("cross-tenant PUT: expected 404, got %d", rec.Code)
	}

	listRec := httptest.NewRecorder()
	listReq, _ := http.NewRequest(http.MethodGet, "/api/v1/invoices", nil)
	listReq.Header.Set("Authorization", authB)
	ts.server.router.ServeHTTP(listRec, listReq)
	if listRec.Code != http.StatusOK {
		t.Fatalf("org B list: expected 200, got %d — body: %s", listRec.Code, listRec.Body.String())
	}
	if contains(invoiceIDsFromList(t, listRec.Body.Bytes()), invoiceA) {
		t.Error("org B's list must not contain org A's invoice")
	}
}

// TestListOutstandingInvoices covers the picker that backs the banking Invoice
// Receipt explanation: only SENT, not-fully-paid invoices, org-scoped. Invoices are
// seeded directly so total/paid/status are exact.
func TestListOutstandingInvoices(t *testing.T) {
	ts := newTestServer(t)
	defer ts.pool.Close()
	ctx := context.Background()

	org, user := newOrgWithOwner(t, ts)
	userID, orgID := mustUUID(t, user), mustUUID(t, org)
	contactID := createContactAs(t, ts, user, org)
	t.Cleanup(func() {
		_, _ = ts.pool.Exec(context.Background(), `DELETE FROM invoices WHERE organisation_id=$1`, org)
	})

	seed := func(orgIDStr, userIDStr, contact, status string, total, paid int64) string {
		var id string
		if err := ts.pool.QueryRow(ctx,
			`INSERT INTO invoices (organisation_id, created_by_user_id, contact_id, dated_on, status,
			        net_value_minor, sales_tax_value_minor, total_value_minor, paid_value_minor, reference)
			 VALUES ($1,$2,$3,CURRENT_DATE,$4,$5,0,$5,$6,$7) RETURNING id::text`,
			orgIDStr, userIDStr, contact, status, total, paid, randomRef()).Scan(&id); err != nil {
			t.Fatalf("seed invoice: %v", err)
		}
		return id
	}

	unpaid := seed(org, user, contactID, "SENT", 10000, 0)
	partial := seed(org, user, contactID, "SENT", 10000, 4000)
	fullyPaid := seed(org, user, contactID, "SENT", 10000, 10000)
	draft := seed(org, user, contactID, "DRAFT", 10000, 0)

	// A SENT-unpaid invoice in ANOTHER org — must never leak into this org's list.
	otherOrg, otherUser := newOrgWithOwner(t, ts)
	otherContact := createContactAs(t, ts, otherUser, otherOrg)
	foreign := seed(otherOrg, otherUser, otherContact, "SENT", 10000, 0)
	t.Cleanup(func() { _, _ = ts.pool.Exec(context.Background(), `DELETE FROM invoices WHERE id=$1`, foreign) })

	list, err := ts.invoiceService.ListOutstandingInvoices(ctx, userID, orgID)
	if err != nil {
		t.Fatalf("list outstanding: %v", err)
	}
	got := map[string]bool{}
	for _, inv := range list {
		got[inv.ID] = true
	}
	if !got[unpaid] || !got[partial] {
		t.Errorf("outstanding should include unpaid + partially-paid; got %v", got)
	}
	if got[fullyPaid] || got[draft] {
		t.Errorf("outstanding should exclude fully-paid + draft; got %v", got)
	}
	if got[foreign] {
		t.Error("outstanding leaked another org's invoice (multi-tenant breach)")
	}
}

// TestReopenGuardWithPayments covers the rule that a SENT invoice with any payment
// recorded against it (paid_value_minor > 0) cannot be reopened to DRAFT — reopening
// is the only route back to editing, and an edit could then make paid exceed the total.
// paid is seeded directly so the guard is exercised at exact unpaid / partial / full
// values; the end-to-end path (a real bank receipt blocks reopen) is in
// TestInvoiceReceiptExplain.
func TestReopenGuardWithPayments(t *testing.T) {
	ts := newTestServer(t)
	defer ts.pool.Close()
	ctx := context.Background()

	org, user := newOrgWithOwner(t, ts)
	userID, orgID := mustUUID(t, user), mustUUID(t, org)
	contactID := createContactAs(t, ts, user, org)
	t.Cleanup(func() {
		_, _ = ts.pool.Exec(context.Background(), `DELETE FROM invoices WHERE organisation_id=$1`, org)
	})

	seedSent := func(total, paid int64) string {
		var id string
		if err := ts.pool.QueryRow(ctx,
			`INSERT INTO invoices (organisation_id, created_by_user_id, contact_id, dated_on, status,
			        net_value_minor, sales_tax_value_minor, total_value_minor, paid_value_minor, reference)
			 VALUES ($1,$2,$3,CURRENT_DATE,'SENT',$4,0,$4,$5,$6) RETURNING id::text`,
			org, user, contactID, total, paid, randomRef()).Scan(&id); err != nil {
			t.Fatalf("seed: %v", err)
		}
		return id
	}

	t.Run("reopen succeeds when nothing is paid", func(t *testing.T) {
		inv := seedSent(10000, 0)
		out, err := ts.invoiceService.ChangeStatus(ctx, userID, orgID, inv, "reopen")
		if err != nil {
			t.Fatalf("reopen: %v", err)
		}
		if out.Status != "DRAFT" {
			t.Errorf("status after reopen: got %q, want DRAFT", out.Status)
		}
	})

	t.Run("reopen is blocked when partially paid", func(t *testing.T) {
		inv := seedSent(10000, 4000)
		_, err := ts.invoiceService.ChangeStatus(ctx, userID, orgID, inv, "reopen")
		assertAppCode(t, err, kernel.ErrCodeConflict)
	})

	t.Run("reopen is blocked when fully paid", func(t *testing.T) {
		inv := seedSent(10000, 10000)
		_, err := ts.invoiceService.ChangeStatus(ctx, userID, orgID, inv, "reopen")
		assertAppCode(t, err, kernel.ErrCodeConflict)
	})
}

// TestInvoiceStatusFiledPeriodLock covers the invoice half of the VAT filed-period
// lock. The return counts status='SENT' invoices, so any transition INTO or OUT OF
// SENT (issue/send add it; reopen/write_off/refund remove it) changes whether a
// dated-in-period invoice is counted. Once a period is filed, those transitions are
// refused (409) for an invoice dated inside it; a transition that doesn't touch SENT
// (schedule), and any transition outside the filed period, is unaffected. The period
// is "filed" by inserting a marked_as_filed vat_returns row.
func TestInvoiceStatusFiledPeriodLock(t *testing.T) {
	ts := newTestServer(t)
	defer ts.pool.Close()

	orgID, ownerID := newOrgWithOwner(t, ts)
	authHeader := bearer(t, ts, ownerID, orgID)

	t.Cleanup(func() {
		c := context.Background()
		_, _ = ts.pool.Exec(c, `DELETE FROM vat_returns WHERE organisation_id=$1`, orgID)
		_, _ = ts.pool.Exec(c, `DELETE FROM invoices WHERE organisation_id=$1`, orgID)
		_, _ = ts.pool.Exec(c, `DELETE FROM contacts WHERE organisation_id=$1`, orgID)
	})

	contactID := vatSeedContact(t, ts, orgID, ownerID)
	seed := func(datedOn, status string) string {
		return vatSeedInvoice(t, ts, orgID, ownerID, contactID, datedOn, 10000, 2000, status)
	}

	// File the Mar–May 2026 quarter: a marked_as_filed snapshot covering that range.
	if _, err := ts.pool.Exec(context.Background(),
		`INSERT INTO vat_returns (organisation_id, created_by_user_id, period_start, period_end, period_key, accounting_basis, filing_status, filed_at)
		 VALUES ($1,$2,'2026-03-01','2026-05-31','2026-05-31','invoice','marked_as_filed', now())`, orgID, ownerID); err != nil {
		t.Fatalf("seed filed vat_return: %v", err)
	}

	t.Run("issuing a DRAFT dated in a filed period → 409", func(t *testing.T) {
		id := seed("2026-04-12", "DRAFT") // DRAFT → SENT would ADD it to the filed return
		rec := statusInvoiceReq(t, ts, id, authHeader, "issue")
		if rec.Code != http.StatusConflict {
			t.Errorf("expected 409, got %d — %s", rec.Code, rec.Body.String())
		}
	})

	t.Run("reopening a SENT invoice dated in a filed period → 409", func(t *testing.T) {
		id := seed("2026-04-20", "SENT") // SENT → DRAFT would REMOVE it from the filed return
		rec := statusInvoiceReq(t, ts, id, authHeader, "reopen")
		if rec.Code != http.StatusConflict {
			t.Errorf("expected 409, got %d — %s", rec.Code, rec.Body.String())
		}
	})

	t.Run("writing off a SENT invoice dated in a filed period → 409", func(t *testing.T) {
		id := seed("2026-04-21", "SENT") // SENT → WRITTEN_OFF also removes it
		rec := statusInvoiceReq(t, ts, id, authHeader, "write_off")
		if rec.Code != http.StatusConflict {
			t.Errorf("expected 409, got %d — %s", rec.Code, rec.Body.String())
		}
	})

	t.Run("scheduling a DRAFT in a filed period is unaffected (never touches SENT)", func(t *testing.T) {
		id := seed("2026-04-12", "DRAFT") // DRAFT → SCHEDULED: neither state is counted
		rec := statusInvoiceReq(t, ts, id, authHeader, "schedule")
		if rec.Code != http.StatusOK {
			t.Errorf("schedule in a filed period should be allowed, got %d — %s", rec.Code, rec.Body.String())
		}
	})

	t.Run("issuing an invoice OUTSIDE the filed period succeeds", func(t *testing.T) {
		id := seed("2026-07-15", "DRAFT") // next quarter, not filed
		rec := statusInvoiceReq(t, ts, id, authHeader, "issue")
		if rec.Code != http.StatusOK {
			t.Errorf("expected 200, got %d — %s", rec.Code, rec.Body.String())
		}
	})
}
