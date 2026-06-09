package main

// server_test.go
// =============================================================================
// Integration test for handleCreateExpense.
//
// This test hits a REAL PostgreSQL database. It does not use mocks.
//
// Why a real database?
//   Mock tests verify that your Go code calls the right functions with the
//   right arguments. They cannot verify that your SQL is correct, that your
//   constraints fire, or that your data survives a round-trip through pgx's
//   type system. Only a real database can tell you those things.
//
// What you need before running this test:
//   1. PostgreSQL running locally (e.g. via Docker: see docker-compose.yml)
//   2. Schema applied: psql $DATABASE_URL -f db/schema/schema.sql
//   3. Seed data inserted: psql $DATABASE_URL -f db/seeds/expense_categories.sql
//      (the test reads category UUIDs from expense_categories — table must not be empty)
//   4. DATABASE_URL set in your .env file
//
// Run with:
//   go test ./... -v -run TestHandleCreateExpense
//
// The -v flag prints each test name and PASS/FAIL. Without it Go only prints
// output on failure.
// =============================================================================

import (
	"bytes"
	"context"
	"encoding/json"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/joho/godotenv"

	expenses "github.com/operationfb/accounting-saas/db/expenses"
	util "github.com/operationfb/accounting-saas/util"
)

// =============================================================================
// TEST SETUP
// =============================================================================

// testServer holds everything needed across tests in this file.
// We build it once in TestMain and reuse it — opening a DB pool is expensive
// and we don't want to do it for every individual test case.
type testServer struct {
	server *Server
	pool   *pgxpool.Pool
}

// newTestServer connects to the real database and builds a Server instance
// configured for testing. It mirrors what main() does, but reads from .env
// and uses gin.SetMode(gin.TestMode) to suppress Gin's debug output.
func newTestServer(t *testing.T) *testServer {
	t.Helper() // marks this as a helper so failures point to the caller, not here

	// Load .env so DATABASE_URL is available via os.Getenv.
	// We ignore the error — in CI the variables are already in the environment
	// and there is no .env file, which is fine.
	_ = godotenv.Load()

	dbURL := os.Getenv("DATABASE_URL")
	if dbURL == "" {
		// t.Skip marks the test as skipped (not failed) when the database is
		// not available. This is friendlier than t.Fatal in environments where
		// the DB isn't set up (e.g. a frontend developer's machine).
		t.Skip("DATABASE_URL not set — skipping database integration test")
	}

	pool, err := pgxpool.New(context.Background(), dbURL)
	if err != nil {
		t.Fatalf("could not connect to database: %v", err)
	}

	if err := pool.Ping(context.Background()); err != nil {
		t.Fatalf("database ping failed: %v", err)
	}

	queries := expenses.New(pool)
	service := NewExpenseService(pool, queries)
	server := NewServer(service)

	return &testServer{
		server: server,
		pool:   pool,
	}
}

func randomCategoryUUID(t *testing.T, pool *pgxpool.Pool) string {
	t.Helper()

	// Fetch all category UUIDs for the stub organisation.
	// We cast id to text so pgx scans it as a plain string — no pgtype.UUID
	// handling needed in test helper code.
	rows, err := pool.Query(context.Background(), `
		SELECT id::text
		FROM expense_categories
		WHERE organisation_id = '00000000-0000-0000-0000-000000000001'
		  AND is_active = TRUE
	`)
	if err != nil {
		t.Fatalf("randomCategoryUUID: query failed: %v", err)
	}
	defer rows.Close()

	// Collect all UUIDs into a slice.
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			t.Fatalf("randomCategoryUUID: scan failed: %v", err)
		}
		ids = append(ids, id)
	}
	// rows.Err() returns any error that occurred during iteration — always
	// check this after a rows loop, not just inside it.
	if err := rows.Err(); err != nil {
		t.Fatalf("randomCategoryUUID: rows iteration error: %v", err)
	}

	if len(ids) == 0 {
		t.Fatal("randomCategoryUUID: expense_categories table is empty — run the seed INSERT script first")
	}

	// Shuffle the slice in place using the Fisher-Yates algorithm.
	// rand.Shuffle takes the slice length and a swap function.
	rand.Shuffle(len(ids), func(i, j int) {
		ids[i], ids[j] = ids[j], ids[i]
	})

	// Return the first element of the shuffled slice.
	return ids[0]
}

// =============================================================================
// TESTS
// =============================================================================

// TestHandleCreateExpense tests POST /api/v1/expenses with valid data.
// It verifies:
//   - HTTP status is 201 Created
//   - The response body contains an "expense" key
//   - The returned expense has the same description and gross_value we sent
//   - A row actually exists in the database after the request
func TestHandleCreateExpense(t *testing.T) {
	ts := newTestServer(t)
	defer ts.pool.Close()

	categoryID := randomCategoryUUID(t, ts.pool)

	// -------------------------------------------------------------------------
	// Build the request body using random data from our util package.
	// Using a fixed organisation_id and user_id is fine here — in our schema
	// they are plain UUID columns with no FK constraint to a users table yet.
	// -------------------------------------------------------------------------
	orgID := "00000000-0000-0000-0000-000000000001" // matches the stub in handleCreateExpense
	userID := "00000000-0000-0000-0000-000000000002"
	grossValue := util.RandomGrossValue()
	description := util.RandomExpenseDescription()
	supplierName := util.RandomSupplierName()
	receiptRef := util.RandomReceiptReference()

	reqBody := CreateExpenseRequest{
		UserID:           userID,
		CategoryID:       categoryID,
		DatedOn:          util.RandomDatedOn(),
		Description:      description,
		CurrencyCode:     "GBP",
		GrossValuePounds: grossValue,
		SupplierName:     &supplierName,
		ReceiptReference: &receiptRef,
	}

	// -------------------------------------------------------------------------
	// Serialise the struct to JSON bytes and wrap in a bytes.Reader so it can
	// be used as an http.Request body. This is exactly what a real HTTP client
	// would send.
	// -------------------------------------------------------------------------
	bodyBytes, err := json.Marshal(reqBody)
	if err != nil {
		t.Fatalf("failed to marshal request body: %v", err)
	}

	// -------------------------------------------------------------------------
	// httptest.NewRecorder() is a fake http.ResponseWriter.
	// It captures the status code, headers, and body that the handler writes,
	// so we can inspect them after the handler returns — no real network needed.
	// -------------------------------------------------------------------------
	recorder := httptest.NewRecorder()

	// Build a real *http.Request pointing at our route.
	req, err := http.NewRequest(http.MethodPost, "/api/v1/expenses", bytes.NewReader(bodyBytes))
	if err != nil {
		t.Fatalf("failed to build request: %v", err)
	}
	// Tell the handler the body is JSON. Without this header Gin's
	// ShouldBindJSON returns an error ("unsupported content type").
	req.Header.Set("Content-Type", "application/json")

	// -------------------------------------------------------------------------
	// Fire the request through the Gin router. ServeHTTP dispatches the request
	// to the matching handler exactly as it would in a live server, including
	// all middleware registered in registerRoutes().
	// -------------------------------------------------------------------------
	ts.server.router.ServeHTTP(recorder, req)

	// -------------------------------------------------------------------------
	// ASSERTION 1: HTTP status must be 201 Created
	// -------------------------------------------------------------------------
	if recorder.Code != http.StatusCreated {
		// Print the response body so we can see the error message when it fails.
		t.Fatalf("expected status 201, got %d — body: %s", recorder.Code, recorder.Body.String())
	}

	// -------------------------------------------------------------------------
	// ASSERTION 2: Parse the response body and check structure
	// -------------------------------------------------------------------------

	// We decode into a generic map first because the response is wrapped:
	// { "expense": { "id": "...", "description": "...", ... } }
	var responseMap map[string]json.RawMessage
	if err := json.NewDecoder(recorder.Body).Decode(&responseMap); err != nil {
		t.Fatalf("failed to decode response body: %v", err)
	}

	// Check the "expense" key exists in the response
	expenseJSON, ok := responseMap["expense"]
	if !ok {
		t.Fatal("response body missing 'expense' key")
	}

	// Decode the expense object itself into our response struct
	var got ExpenseResponse
	if err := json.Unmarshal(expenseJSON, &got); err != nil {
		t.Fatalf("failed to unmarshal expense from response: %v", err)
	}

	// -------------------------------------------------------------------------
	// ASSERTION 3: Returned fields match what we sent
	// -------------------------------------------------------------------------
	if got.Description != description {
		t.Errorf("description: got %q, want %q", got.Description, description)
	}

	if got.GrossValue != grossValue {
		t.Errorf("gross_value: got %q, want %q", got.GrossValue, grossValue)
	}

	if got.OrganisationID != orgID {
		t.Errorf("organisation_id: got %q, want %q", got.OrganisationID, orgID)
	}

	if got.Status != "DRAFT" {
		t.Errorf("status: got %q, want %q", got.Status, "DRAFT")
	}

	if got.ID == "" {
		t.Error("id: expected a non-empty UUID, got empty string")
	}

	// -------------------------------------------------------------------------
	// ASSERTION 4: Row actually exists in the database
	//
	// This is the key assertion that only a real database can give you.
	// We query PostgreSQL directly to confirm the row was committed, not just
	// returned by the handler. A bug where the handler fakes a response without
	// writing to the DB would pass assertions 1-3 but fail here.
	// -------------------------------------------------------------------------
	var dbDescription string
	err = ts.pool.QueryRow(context.Background(),
		"SELECT description FROM expenses WHERE id = $1 AND organisation_id = $2",
		got.ID, orgID,
	).Scan(&dbDescription)

	if err != nil {
		t.Fatalf("expense not found in database (id=%s): %v", got.ID, err)
	}

	if dbDescription != description {
		t.Errorf("database description: got %q, want %q", dbDescription, description)
	}

	// -------------------------------------------------------------------------
	// Cleanup: delete the expense we just created so the test is idempotent.
	// t.Cleanup runs after the test regardless of pass/fail.
	/* -------------------------------------------------------------------------
	t.Cleanup(func() {
		_, _ = ts.pool.Exec(context.Background(),
			"DELETE FROM expenses WHERE id = $1", got.ID)
	})
	*/
}

// TestHandleCreateExpense_MissingDescription tests that a request with no
// description returns 400 Bad Request, not 500.
// This tests the validation layer in the handler, not the service.
func TestHandleCreateExpense_MissingDescription(t *testing.T) {
	ts := newTestServer(t)
	defer ts.pool.Close()

	// Deliberately omit description — it is `binding:"required"` in the struct
	body := map[string]string{
		"user_id":     "00000000-0000-0000-0000-000000000002",
		"category_id": "7200",
		"dated_on":    util.RandomDatedOn(),
		"currency":    "GBP",
		"gross_value": util.RandomGrossValue(),
		// "description" intentionally missing
	}

	bodyBytes, _ := json.Marshal(body)
	recorder := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodPost, "/api/v1/expenses", bytes.NewReader(bodyBytes))
	req.Header.Set("Content-Type", "application/json")

	ts.server.router.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusBadRequest {
		t.Errorf("expected status 400, got %d — body: %s", recorder.Code, recorder.Body.String())
	}
}

// TestHandleCreateExpense_InvalidGrossValue tests that a non-numeric gross_value
// returns 500 (service returns an error) rather than panicking.
// Once we add proper error types this should return 422 Unprocessable Entity.
func TestHandleCreateExpense_InvalidGrossValue(t *testing.T) {
	ts := newTestServer(t)
	defer ts.pool.Close()

	categoryID := randomCategoryUUID(t, ts.pool)

	body := CreateExpenseRequest{
		UserID:           "00000000-0000-0000-0000-000000000002",
		CategoryID:       categoryID,
		DatedOn:          util.RandomDatedOn(),
		Description:      util.RandomExpenseDescription(),
		CurrencyCode:     "GBP",
		GrossValuePounds: "not-a-number", // invalid
	}

	bodyBytes, _ := json.Marshal(body)
	recorder := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodPost, "/api/v1/expenses", bytes.NewReader(bodyBytes))
	req.Header.Set("Content-Type", "application/json")

	ts.server.router.ServeHTTP(recorder, req)

	// Currently returns 500 because we have no error type mapping yet.
	// TODO: once AppError is implemented this should assert 422.
	if recorder.Code == http.StatusCreated {
		t.Error("expected a non-201 status for invalid gross_value, got 201")
	}
}
