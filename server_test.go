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
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/joho/godotenv"

	"github.com/operationfb/accounting-saas/db/auth"
	expenses "github.com/operationfb/accounting-saas/db/expenses"
	"github.com/operationfb/accounting-saas/token"
	util "github.com/operationfb/accounting-saas/util"
)

// =============================================================================
// TEST SETUP
// =============================================================================

// testServer holds everything needed across tests in this file.
// We build it once in TestMain and reuse it — opening a DB pool is expensive
// and we don't want to do it for every individual test case.
type testServer struct {
	server     *Server
	pool       *pgxpool.Pool
	tokenMaker token.Maker
}

// testSymmetricKey is a fixed 32-byte key used only by tests to build a PASETO
// token maker. The login tests only check that a token is issued and round-trips,
// so the key value is irrelevant — it just has to be the right length.
const testSymmetricKey = "12345678901234567890123456789012"

// testCORSOrigin is the single allowed CORS origin the test server is built with.
const testCORSOrigin = "http://localhost:3000"

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
	authQueries := auth.New(pool)
	service := NewExpenseService(pool, queries, authQueries)

	// Build a real auth handler so the /auth/* routes work, and pass the token
	// maker to the server so the expense routes' auth middleware can verify
	// tokens. The maker uses a fixed test key — these tests only check that a
	// token is issued and round-trips, not that it was signed with the
	// production key.
	tokenMaker, err := token.NewPasetoMaker([]byte(testSymmetricKey))
	if err != nil {
		t.Fatalf("failed to create token maker: %v", err)
	}
	authHandler := NewAuthHandler(authQueries, tokenMaker, time.Minute)
	server := NewServer(service, authHandler, tokenMaker, []string{testCORSOrigin})

	return &testServer{
		server:     server,
		pool:       pool,
		tokenMaker: tokenMaker,
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
// AUTH TEST HELPERS
// =============================================================================

// devUserID / devOrgID are the seeded development user and organisation. The
// dev user is an active OWNER of the dev org, so it exercises the admin path.
const (
	devUserID = "00000000-0000-0000-0000-000000000002"
	devOrgID  = "00000000-0000-0000-0000-000000000001"
)

// bearer builds an "Authorization: Bearer <token>" header value for the given
// user and organisation. Handlers behind authMiddleware require this header.
func bearer(t *testing.T, ts *testServer, userID, orgID string) string {
	t.Helper()
	uid, err := uuid.Parse(userID)
	if err != nil {
		t.Fatalf("bearer: bad userID %q: %v", userID, err)
	}
	oid, err := uuid.Parse(orgID)
	if err != nil {
		t.Fatalf("bearer: bad orgID %q: %v", orgID, err)
	}
	tok, err := ts.tokenMaker.CreateToken(uid, oid, time.Minute)
	if err != nil {
		t.Fatalf("bearer: create token: %v", err)
	}
	return "Bearer " + tok
}

// createExpenseAs creates an expense through the API as the given user/org and
// returns the new expense id.
func createExpenseAs(t *testing.T, ts *testServer, userID, orgID string) string {
	t.Helper()
	categoryID := randomCategoryUUID(t, ts.pool)
	reqBody := CreateExpenseRequest{
		CategoryID:       categoryID,
		DatedOn:          util.RandomDatedOn(),
		Description:      util.RandomExpenseDescription(),
		CurrencyCode:     "GBP",
		GrossValuePounds: util.RandomGrossValue(),
	}
	bodyBytes, _ := json.Marshal(reqBody)
	recorder := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodPost, "/api/v1/expenses", bytes.NewReader(bodyBytes))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", bearer(t, ts, userID, orgID))
	ts.server.router.ServeHTTP(recorder, req)
	if recorder.Code != http.StatusCreated {
		t.Fatalf("createExpenseAs: expected 201, got %d — body: %s", recorder.Code, recorder.Body.String())
	}
	var resp map[string]ExpenseResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &resp); err != nil {
		t.Fatalf("createExpenseAs: decode: %v", err)
	}
	return resp["expense"].ID
}

// newMemberUser inserts an ephemeral active 'member' user into the org and
// registers cleanup that removes any expenses they created, their membership,
// and the user row. Returns the new user's id.
func newMemberUser(t *testing.T, ts *testServer, orgID string) string {
	t.Helper()
	id := uuid.NewString()
	email := "member-" + id + "@test.local"
	ctx := context.Background()
	if _, err := ts.pool.Exec(ctx,
		`INSERT INTO users (id, email, first_name, last_name, is_active, email_verified_at)
		 VALUES ($1, $2, 'Test', 'Member', TRUE, now())`, id, email); err != nil {
		t.Fatalf("newMemberUser: insert user: %v", err)
	}
	if _, err := ts.pool.Exec(ctx,
		`INSERT INTO organisation_memberships (organisation_id, user_id, role, status)
		 VALUES ($1, $2, 'member', 'active')`, orgID, id); err != nil {
		t.Fatalf("newMemberUser: insert membership: %v", err)
	}
	t.Cleanup(func() {
		_, _ = ts.pool.Exec(ctx, `DELETE FROM expenses WHERE user_id = $1`, id)
		_, _ = ts.pool.Exec(ctx, `DELETE FROM organisation_memberships WHERE user_id = $1`, id)
		_, _ = ts.pool.Exec(ctx, `DELETE FROM users WHERE id = $1`, id)
	})
	return id
}

// expenseIDsFromList decodes { "expenses": [ {id...}, ... ] } into a slice of ids.
func expenseIDsFromList(t *testing.T, body []byte) []string {
	t.Helper()
	var resp struct {
		Expenses []ExpenseResponse `json:"expenses"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		t.Fatalf("expenseIDsFromList: decode: %v", err)
	}
	ids := make([]string, 0, len(resp.Expenses))
	for _, e := range resp.Expenses {
		ids = append(ids, e.ID)
	}
	return ids
}

func contains(ss []string, s string) bool {
	for _, v := range ss {
		if v == s {
			return true
		}
	}
	return false
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
	// Authenticate as the dev user (owner of the dev org). The handler derives
	// the claimant + organisation from this token, not from the body.
	req.Header.Set("Authorization", bearer(t, ts, userID, orgID))

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

	// The claimant must come from the token, not the request body.
	if got.UserID != userID {
		t.Errorf("user_id: got %q, want %q (must come from the token)", got.UserID, userID)
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
	req.Header.Set("Authorization", bearer(t, ts, devUserID, devOrgID))

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
	req.Header.Set("Authorization", bearer(t, ts, devUserID, devOrgID))

	ts.server.router.ServeHTTP(recorder, req)

	// gross_value "not-a-number" fails decimal parsing in the service, which
	// returns ErrValidation → 422 Unprocessable Entity.
	if recorder.Code != http.StatusUnprocessableEntity {
		t.Errorf("expected status 422, got %d — body: %s", recorder.Code, recorder.Body.String())
	}
}

// =============================================================================
// AUTH — LOGIN
// =============================================================================

// TestHandleLoginUser tests POST /api/v1/auth/login with the seeded dev user.
// It verifies:
//   - HTTP status is 200 OK
//   - A non-empty PASETO access token is returned and round-trips through the
//     same token maker, carrying the user's id
//   - The user object echoes the right email and a non-empty id
//   - The user object does NOT leak sensitive fields (password_hash, timestamps,
//     security counters, ...)
func TestHandleLoginUser(t *testing.T) {
	ts := newTestServer(t)
	defer ts.pool.Close()

	const (
		devEmail    = "dev@example.com"
		devPassword = "devpassword123"
	)
	/*
		// Arrange: the seed row for dev@example.com ships with a PLACEHOLDER hash
		// that does not actually match devpassword123. Set a correct bcrypt hash
		// (cost 12, as the schema documents) so the documented dev credentials work.
		// This is idempotent and safe to run on every test invocation.
		hashed, err := bcrypt.GenerateFromPassword([]byte(devPassword), 12)
		if err != nil {
			t.Fatalf("failed to hash dev password: %v", err)
		}
		if _, err := ts.pool.Exec(context.Background(),
			"UPDATE users SET password_hash = $1 WHERE email = $2", string(hashed), devEmail); err != nil {
			t.Fatalf("failed to set dev user password: %v", err)
		}
	*/
	// Act: send the login request through the router.
	bodyBytes, _ := json.Marshal(map[string]string{"email": devEmail, "password": devPassword})
	recorder := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodPost, "/api/v1/auth/login", bytes.NewReader(bodyBytes))
	req.Header.Set("Content-Type", "application/json")
	ts.server.router.ServeHTTP(recorder, req)

	// Assert: 200 OK.
	if recorder.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d — body: %s", recorder.Code, recorder.Body.String())
	}

	// Decode the typed response.
	var got loginUserResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &got); err != nil {
		t.Fatalf("failed to decode login response: %v", err)
	}

	// Token must be present and must verify against the same maker, carrying the
	// returned user's id.
	if got.AccessToken == "" {
		t.Fatal("access_token: expected a non-empty token")
	}
	payload, err := ts.tokenMaker.VerifyToken(got.AccessToken)
	if err != nil {
		t.Fatalf("returned token failed verification: %v", err)
	}
	if payload.UserID.String() != got.User.ID {
		t.Errorf("token user_id: got %q, want %q", payload.UserID.String(), got.User.ID)
	}

	// User fields.
	if got.User.Email != devEmail {
		t.Errorf("user.email: got %q, want %q", got.User.Email, devEmail)
	}
	if got.User.ID == "" {
		t.Error("user.id: expected a non-empty UUID")
	}

	// The user object must not leak sensitive fields.
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(recorder.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("failed to decode response envelope: %v", err)
	}
	var userObj map[string]json.RawMessage
	if err := json.Unmarshal(envelope["user"], &userObj); err != nil {
		t.Fatalf("failed to decode user object: %v", err)
	}
	for _, banned := range []string{
		"password", "password_hash", "created_at", "updated_at", "deleted_at",
		"failed_login_count", "locked_until", "last_login_ip", "last_login_at",
		"email_verification_token", "password_reset_token",
	} {
		if _, leaked := userObj[banned]; leaked {
			t.Errorf("login response leaks sensitive user field %q", banned)
		}
	}
}

// TestHandleLoginUser_WrongPassword verifies a bad password is rejected with
// 401 Unauthorized (no token issued).
func TestHandleLoginUser_WrongPassword(t *testing.T) {
	ts := newTestServer(t)
	defer ts.pool.Close()

	bodyBytes, _ := json.Marshal(map[string]string{
		"email":    "dev@example.com",
		"password": "definitely-the-wrong-password",
	})
	recorder := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodPost, "/api/v1/auth/login", bytes.NewReader(bodyBytes))
	req.Header.Set("Content-Type", "application/json")
	ts.server.router.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("expected status 401, got %d — body: %s", recorder.Code, recorder.Body.String())
	}
}

// =============================================================================
// EXPENSES — AUTHORIZATION
// =============================================================================

// TestExpenses_RequireAuth verifies the expense routes reject requests with no
// (or a malformed) bearer token with 401, before any handler logic runs.
func TestExpenses_RequireAuth(t *testing.T) {
	ts := newTestServer(t)
	defer ts.pool.Close()

	cases := []struct {
		name   string
		method string
		path   string
		header string
	}{
		{"list no token", http.MethodGet, "/api/v1/expenses", ""},
		{"get no token", http.MethodGet, "/api/v1/expenses/" + devOrgID, ""},
		{"create no token", http.MethodPost, "/api/v1/expenses", ""},
		{"create bad scheme", http.MethodPost, "/api/v1/expenses", "Basic abc"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			req, _ := http.NewRequest(tc.method, tc.path, nil)
			if tc.header != "" {
				req.Header.Set("Authorization", tc.header)
			}
			ts.server.router.ServeHTTP(recorder, req)
			if recorder.Code != http.StatusUnauthorized {
				t.Errorf("expected 401, got %d — body: %s", recorder.Code, recorder.Body.String())
			}
		})
	}
}

// TestHandleListExpenses_OwnerSeesAll verifies an owner/admin can list expenses
// for the whole organisation. The dev user is an owner of the dev org.
func TestHandleListExpenses_OwnerSeesAll(t *testing.T) {
	ts := newTestServer(t)
	defer ts.pool.Close()

	// Create an expense as the dev owner so the list is non-empty.
	created := createExpenseAs(t, ts, devUserID, devOrgID)

	recorder := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodGet, "/api/v1/expenses", nil)
	req.Header.Set("Authorization", bearer(t, ts, devUserID, devOrgID))
	ts.server.router.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d — body: %s", recorder.Code, recorder.Body.String())
	}
	ids := expenseIDsFromList(t, recorder.Body.Bytes())
	if !contains(ids, created) {
		t.Errorf("owner's list should contain the expense it just created (%s)", created)
	}
}

// TestExpenseOwnership_MemberScoped verifies a plain member sees only their own
// expenses and is refused (403) when reading another user's expense.
func TestExpenseOwnership_MemberScoped(t *testing.T) {
	ts := newTestServer(t)
	defer ts.pool.Close()

	// Owner creates an expense the member must NOT be able to see.
	ownerExpense := createExpenseAs(t, ts, devUserID, devOrgID)

	// Create an ephemeral active 'member' of the same org (cleaned up on exit).
	memberID := newMemberUser(t, ts, devOrgID)

	// Member creates their own expense.
	memberExpense := createExpenseAs(t, ts, memberID, devOrgID)

	// 1) Member lists → sees their own, not the owner's.
	recorder := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodGet, "/api/v1/expenses", nil)
	req.Header.Set("Authorization", bearer(t, ts, memberID, devOrgID))
	ts.server.router.ServeHTTP(recorder, req)
	if recorder.Code != http.StatusOK {
		t.Fatalf("member list: expected 200, got %d — body: %s", recorder.Code, recorder.Body.String())
	}
	ids := expenseIDsFromList(t, recorder.Body.Bytes())
	if !contains(ids, memberExpense) {
		t.Errorf("member list should contain their own expense %s", memberExpense)
	}
	if contains(ids, ownerExpense) {
		t.Errorf("member list must NOT contain the owner's expense %s", ownerExpense)
	}

	// 2) Member reads the owner's expense → 403.
	recorder = httptest.NewRecorder()
	req, _ = http.NewRequest(http.MethodGet, "/api/v1/expenses/"+ownerExpense, nil)
	req.Header.Set("Authorization", bearer(t, ts, memberID, devOrgID))
	ts.server.router.ServeHTTP(recorder, req)
	if recorder.Code != http.StatusForbidden {
		t.Errorf("member reading owner's expense: expected 403, got %d — body: %s", recorder.Code, recorder.Body.String())
	}

	// 3) Member reads their own expense → 200.
	recorder = httptest.NewRecorder()
	req, _ = http.NewRequest(http.MethodGet, "/api/v1/expenses/"+memberExpense, nil)
	req.Header.Set("Authorization", bearer(t, ts, memberID, devOrgID))
	ts.server.router.ServeHTTP(recorder, req)
	if recorder.Code != http.StatusOK {
		t.Errorf("member reading own expense: expected 200, got %d — body: %s", recorder.Code, recorder.Body.String())
	}
}

// =============================================================================
// CORS
// =============================================================================

// TestCORS verifies the CORS policy: a preflight OPTIONS is answered (204)
// before the auth middleware runs, the allowed origin is echoed on real
// requests, and a disallowed origin is not echoed.
func TestCORS(t *testing.T) {
	ts := newTestServer(t)
	defer ts.pool.Close()

	t.Run("preflight bypasses auth", func(t *testing.T) {
		recorder := httptest.NewRecorder()
		req, _ := http.NewRequest(http.MethodOptions, "/api/v1/expenses", nil)
		req.Header.Set("Origin", testCORSOrigin)
		req.Header.Set("Access-Control-Request-Method", "POST")
		req.Header.Set("Access-Control-Request-Headers", "authorization,content-type")
		ts.server.router.ServeHTTP(recorder, req)

		// 204 from the CORS middleware — NOT 401 from authMiddleware. This proves
		// CORS runs before auth, so preflight (which carries no token) succeeds.
		if recorder.Code != http.StatusNoContent {
			t.Fatalf("preflight: expected 204, got %d — body: %s", recorder.Code, recorder.Body.String())
		}
		if got := recorder.Header().Get("Access-Control-Allow-Origin"); got != testCORSOrigin {
			t.Errorf("Access-Control-Allow-Origin: got %q, want %q", got, testCORSOrigin)
		}
		if allow := recorder.Header().Get("Access-Control-Allow-Headers"); !strings.Contains(strings.ToLower(allow), "authorization") {
			t.Errorf("Access-Control-Allow-Headers %q must include Authorization", allow)
		}
	})

	t.Run("allowed origin on real request", func(t *testing.T) {
		recorder := httptest.NewRecorder()
		req, _ := http.NewRequest(http.MethodGet, "/api/v1/expenses", nil)
		req.Header.Set("Origin", testCORSOrigin)
		req.Header.Set("Authorization", bearer(t, ts, devUserID, devOrgID))
		ts.server.router.ServeHTTP(recorder, req)

		if recorder.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d — body: %s", recorder.Code, recorder.Body.String())
		}
		if got := recorder.Header().Get("Access-Control-Allow-Origin"); got != testCORSOrigin {
			t.Errorf("Access-Control-Allow-Origin: got %q, want %q", got, testCORSOrigin)
		}
	})

	t.Run("disallowed origin not echoed", func(t *testing.T) {
		recorder := httptest.NewRecorder()
		req, _ := http.NewRequest(http.MethodOptions, "/api/v1/expenses", nil)
		req.Header.Set("Origin", "http://evil.example")
		req.Header.Set("Access-Control-Request-Method", "POST")
		ts.server.router.ServeHTTP(recorder, req)

		if got := recorder.Header().Get("Access-Control-Allow-Origin"); got == "http://evil.example" {
			t.Errorf("disallowed origin must not be echoed, got Access-Control-Allow-Origin %q", got)
		}
	})
}
