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
	"encoding/csv"
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
	"golang.org/x/crypto/bcrypt"

	"github.com/operationfb/accounting-saas/db/auth"
	contacts "github.com/operationfb/accounting-saas/db/contacts"
	emailinbox "github.com/operationfb/accounting-saas/db/email_inbox"
	expenses "github.com/operationfb/accounting-saas/db/expenses"
	projectsdb "github.com/operationfb/accounting-saas/db/projects"
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
	server            *Server
	pool              *pgxpool.Pool
	tokenMaker        token.Maker
	emailSender       *fakeEmailSender
	emailInboxService *EmailInboxService
}

// testSymmetricKey is a fixed 32-byte key used only by tests to build a PASETO
// token maker. The login tests only check that a token is issued and round-trips,
// so the key value is irrelevant — it just has to be the right length.
const testSymmetricKey = "12345678901234567890123456789012"

// testCORSOrigin is the single allowed CORS origin the test server is built with.
const testCORSOrigin = "http://localhost:3000"

// testAppBaseURL is the frontend base the test server builds reset links against.
const testAppBaseURL = "http://localhost:5173"

// testInboxDomain is the receipt-inbox domain the test email-to-expense channel
// is built with, e.g. "alpha@receipts.test".
const testInboxDomain = "receipts.test"

// testMailgunSigningKey is the fixed HMAC key the test server verifies inbound
// webhooks against, so signature tests can compute a valid signature.
const testMailgunSigningKey = "test-mailgun-signing-key"

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
	emailSender := &fakeEmailSender{}
	authHandler := NewAuthHandler(authQueries, tokenMaker, time.Minute, emailSender, testAppBaseURL, 15*time.Minute)

	// Attachment storage: when GCS_BUCKET is set the tests exercise the real GCS
	// code path against that bucket; otherwise storage is nil and the attachment
	// tests skip (see requireGCS in attachment_service_test.go).
	var store Storage
	if bucket := os.Getenv("GCS_BUCKET"); bucket != "" {
		gcs, gcsErr := newGCSStorage(context.Background(), bucket)
		if gcsErr != nil {
			t.Fatalf("failed to create GCS storage: %v", gcsErr)
		}
		store = gcs
	}
	attachmentService := NewAttachmentService(pool, queries, authQueries, store, nil, 0, 0)
	contactService := NewContactService(pool, contacts.New(pool), authQueries)
	projectService := NewProjectService(pool, projectsdb.New(pool), authQueries, contacts.New(pool))
	memberService := NewMemberService(authQueries)
	organisationService := NewOrganisationService(authQueries)
	userService := NewUserService(authQueries)
	// Email-to-expense: wire a real service with a FAKE HTML renderer (so HTML-body
	// tests don't need a Gotenberg server) and a fixed signing key (so signature
	// tests can compute a valid HMAC). Capture still flows through the real
	// attachment service, so attachment/HTML tests require GCS like other captures.
	emailInboxService := NewEmailInboxService(authQueries, emailinbox.New(pool), attachmentService, &fakeHTMLRenderer{pdf: samplePDF()}, testInboxDomain)
	server := NewServer(service, attachmentService, contactService, projectService, memberService, organisationService, userService, emailInboxService, authHandler, tokenMaker, testMailgunSigningKey, []string{testCORSOrigin})

	return &testServer{
		server:            server,
		pool:              pool,
		tokenMaker:        tokenMaker,
		emailSender:       emailSender,
		emailInboxService: emailInboxService,
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
	// Arrange: the seed (db/schema/auth_schema.sql) already hashes dev@example.com's
	// password to devpassword123, but the SHARED dev DB row can drift if someone
	// changes that password out of band. Re-assert the documented credential here
	// (idempotent, bcrypt cost 12) so this test — and TestHandleLoginUser_NoOrganisationFails,
	// which copies this row's hash — stay green regardless of that drift.
	hashed, err := bcrypt.GenerateFromPassword([]byte(devPassword), 12)
	if err != nil {
		t.Fatalf("failed to hash dev password: %v", err)
	}
	if _, err := ts.pool.Exec(context.Background(),
		"UPDATE users SET password_hash = $1 WHERE email = $2", string(hashed), devEmail); err != nil {
		t.Fatalf("failed to set dev user password: %v", err)
	}

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

	// The response must carry the organisation the session is scoped to,
	// including its country_code — a must-have that drives country-scoped
	// features (VAT rates). The seeded dev org is 'GB'.
	if got.Organisation == nil {
		t.Fatal("organisation: expected the login response to include the scoped organisation")
	}
	if got.Organisation.ID != devOrgID {
		t.Errorf("organisation.id: got %q, want %q", got.Organisation.ID, devOrgID)
	}
	if got.Organisation.CountryCode != "GB" {
		t.Errorf("organisation.country_code: got %q, want %q", got.Organisation.CountryCode, "GB")
	}
	// The response must carry the caller's membership role in the scoped org so
	// the frontend can drive role-based UI. The seeded dev user is the org owner.
	if got.Organisation.Role != "owner" {
		t.Errorf("organisation.role: got %q, want %q", got.Organisation.Role, "owner")
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

// TestHandleLoginUser_NoOrganisationFails verifies the country_code must-have:
// a user who authenticates correctly but belongs to NO organisation (so no
// country_code can be resolved) is refused, and no token is issued.
func TestHandleLoginUser_NoOrganisationFails(t *testing.T) {
	ts := newTestServer(t)
	defer ts.pool.Close()

	ctx := context.Background()

	// Create an ephemeral, verified, active user with NO membership. We copy the
	// dev user's bcrypt hash so the password 'devpassword123' authenticates
	// without needing bcrypt in the test (same dependency as TestHandleLoginUser).
	id := uuid.NewString()
	email := "no-org-" + id + "@test.local"
	if _, err := ts.pool.Exec(ctx,
		`INSERT INTO users (id, email, password_hash, first_name, last_name, is_active, email_verified_at)
		 SELECT $1, $2, password_hash, 'No', 'Org', TRUE, now()
		 FROM users WHERE email = 'dev@example.com'`, id, email); err != nil {
		t.Fatalf("insert no-org user: %v", err)
	}
	t.Cleanup(func() { _, _ = ts.pool.Exec(ctx, `DELETE FROM users WHERE id = $1`, id) })

	bodyBytes, _ := json.Marshal(map[string]string{"email": email, "password": "devpassword123"})
	recorder := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodPost, "/api/v1/auth/login", bytes.NewReader(bodyBytes))
	req.Header.Set("Content-Type", "application/json")
	ts.server.router.ServeHTTP(recorder, req)

	// Credentials are valid, but no organisation → no country_code → login fails.
	// The guard treats this as a server-side invariant violation (500), and no
	// access token is issued.
	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500 (no organisation → no country_code), got %d — body: %s", recorder.Code, recorder.Body.String())
	}
	if strings.Contains(recorder.Body.String(), "access_token") {
		t.Error("no token must be issued when country_code cannot be resolved")
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
		{"export no token", http.MethodPost, "/api/v1/expenses/export", ""},
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
// CREATE ON BEHALF — an owner/admin records an expense for another user.
// The body's user_id picks the claimant; created_by_user_id stays the caller.
// =============================================================================

// strPtr returns a pointer to s — for setting optional *string request fields.
func strPtr(s string) *string { return &s }

// newDeactivatedMemberUser mirrors newMemberUser but inserts the membership with
// status='deactivated', to prove a non-active claimant is rejected. Same cleanup.
func newDeactivatedMemberUser(t *testing.T, ts *testServer, orgID string) string {
	t.Helper()
	id := uuid.NewString()
	email := "deactivated-" + id + "@test.local"
	ctx := context.Background()
	if _, err := ts.pool.Exec(ctx,
		`INSERT INTO users (id, email, first_name, last_name, is_active, email_verified_at)
		 VALUES ($1, $2, 'Test', 'Deactivated', TRUE, now())`, id, email); err != nil {
		t.Fatalf("newDeactivatedMemberUser: insert user: %v", err)
	}
	if _, err := ts.pool.Exec(ctx,
		`INSERT INTO organisation_memberships (organisation_id, user_id, role, status)
		 VALUES ($1, $2, 'member', 'deactivated')`, orgID, id); err != nil {
		t.Fatalf("newDeactivatedMemberUser: insert membership: %v", err)
	}
	t.Cleanup(func() {
		_, _ = ts.pool.Exec(ctx, `DELETE FROM expenses WHERE user_id = $1`, id)
		_, _ = ts.pool.Exec(ctx, `DELETE FROM organisation_memberships WHERE user_id = $1`, id)
		_, _ = ts.pool.Exec(ctx, `DELETE FROM users WHERE id = $1`, id)
	})
	return id
}

// createdByUserID reads an expense's recorder (created_by_user_id) straight from
// the DB — it is deliberately not exposed in the API response.
func createdByUserID(t *testing.T, ts *testServer, expenseID string) string {
	t.Helper()
	var createdBy string
	if err := ts.pool.QueryRow(context.Background(),
		"SELECT created_by_user_id::text FROM expenses WHERE id = $1", expenseID).Scan(&createdBy); err != nil {
		t.Fatalf("read created_by_user_id for %s: %v", expenseID, err)
	}
	return createdBy
}

// onBehalfBody builds a minimal valid create body that claims for claimantID.
func onBehalfBody(t *testing.T, ts *testServer, claimantID string) CreateExpenseRequest {
	t.Helper()
	return CreateExpenseRequest{
		CategoryID:       randomCategoryUUID(t, ts.pool),
		DatedOn:          util.RandomDatedOn(),
		Description:      util.RandomExpenseDescription(),
		CurrencyCode:     "GBP",
		GrossValuePounds: util.RandomGrossValue(),
		UserID:           strPtr(claimantID),
	}
}

func TestHandleCreateExpense_OnBehalf(t *testing.T) {
	ts := newTestServer(t)
	defer ts.pool.Close()

	t.Run("owner records on behalf of a member", func(t *testing.T) {
		member := newMemberUser(t, ts, devOrgID)

		rec := postExpense(t, ts, bearer(t, ts, devUserID, devOrgID), onBehalfBody(t, ts, member))
		if rec.Code != http.StatusCreated {
			t.Fatalf("expected 201, got %d — body: %s", rec.Code, rec.Body.String())
		}
		got := decodeExpense(t, rec)
		// The claimant is the member...
		if got.UserID != member {
			t.Errorf("user_id (claimant): got %q, want member %q", got.UserID, member)
		}
		// ...but the recorder is the owner who actually typed it.
		if cb := createdByUserID(t, ts, got.ID); cb != devUserID {
			t.Errorf("created_by_user_id: got %q, want caller/owner %q", cb, devUserID)
		}
	})

	t.Run("member cannot record on behalf of another user", func(t *testing.T) {
		member := newMemberUser(t, ts, devOrgID)

		// Member tries to claim it for the owner (someone else) → 403.
		rec := postExpense(t, ts, bearer(t, ts, member, devOrgID), onBehalfBody(t, ts, devUserID))
		if rec.Code != http.StatusForbidden {
			t.Errorf("member on behalf of another: expected 403, got %d — body: %s", rec.Code, rec.Body.String())
		}
	})

	t.Run("owner on behalf of a non-member is rejected", func(t *testing.T) {
		stranger := uuid.NewString() // a user id with no membership in this org

		rec := postExpense(t, ts, bearer(t, ts, devUserID, devOrgID), onBehalfBody(t, ts, stranger))
		if rec.Code != http.StatusUnprocessableEntity {
			t.Errorf("non-member claimant: expected 422, got %d — body: %s", rec.Code, rec.Body.String())
		}
	})

	t.Run("owner on behalf of a deactivated member is rejected", func(t *testing.T) {
		deactivated := newDeactivatedMemberUser(t, ts, devOrgID)

		rec := postExpense(t, ts, bearer(t, ts, devUserID, devOrgID), onBehalfBody(t, ts, deactivated))
		if rec.Code != http.StatusUnprocessableEntity {
			t.Errorf("deactivated claimant: expected 422, got %d — body: %s", rec.Code, rec.Body.String())
		}
	})

	t.Run("omitted user_id defaults to the caller", func(t *testing.T) {
		body := onBehalfBody(t, ts, "") // start from a valid body...
		body.UserID = nil               // ...then omit the claimant entirely.

		rec := postExpense(t, ts, bearer(t, ts, devUserID, devOrgID), body)
		if rec.Code != http.StatusCreated {
			t.Fatalf("expected 201, got %d — body: %s", rec.Code, rec.Body.String())
		}
		got := decodeExpense(t, rec)
		// This expense is owned by the dev owner, so no member-helper cleans it up.
		t.Cleanup(func() { _, _ = ts.pool.Exec(context.Background(), `DELETE FROM expenses WHERE id = $1`, got.ID) })

		if got.UserID != devUserID {
			t.Errorf("claimant should default to caller: got %q, want %q", got.UserID, devUserID)
		}
		if cb := createdByUserID(t, ts, got.ID); cb != devUserID {
			t.Errorf("created_by should be caller: got %q, want %q", cb, devUserID)
		}
	})

	t.Run("member may pass their own id (not on-behalf, so not forbidden)", func(t *testing.T) {
		member := newMemberUser(t, ts, devOrgID)

		// Passing your own user_id is a no-op claimant-wise and must NOT require admin.
		rec := postExpense(t, ts, bearer(t, ts, member, devOrgID), onBehalfBody(t, ts, member))
		if rec.Code != http.StatusCreated {
			t.Fatalf("member self-claim: expected 201, got %d — body: %s", rec.Code, rec.Body.String())
		}
		if got := decodeExpense(t, rec); got.UserID != member {
			t.Errorf("user_id: got %q, want member %q", got.UserID, member)
		}
	})

	t.Run("detail endpoint exposes the claimant", func(t *testing.T) {
		member := newMemberUser(t, ts, devOrgID)

		rec := postExpense(t, ts, bearer(t, ts, devUserID, devOrgID), onBehalfBody(t, ts, member))
		if rec.Code != http.StatusCreated {
			t.Fatalf("setup create: expected 201, got %d — body: %s", rec.Code, rec.Body.String())
		}
		id := decodeExpense(t, rec).ID

		// GET the detail as the owner and confirm user_id is the member.
		getRec := httptest.NewRecorder()
		req, _ := http.NewRequest(http.MethodGet, "/api/v1/expenses/"+id, nil)
		req.Header.Set("Authorization", bearer(t, ts, devUserID, devOrgID))
		ts.server.router.ServeHTTP(getRec, req)
		if getRec.Code != http.StatusOK {
			t.Fatalf("get detail: expected 200, got %d — body: %s", getRec.Code, getRec.Body.String())
		}
		var resp struct {
			Expense ExpenseDetailResponse `json:"expense"`
		}
		if err := json.Unmarshal(getRec.Body.Bytes(), &resp); err != nil {
			t.Fatalf("decode detail: %v — body: %s", err, getRec.Body.String())
		}
		if resp.Expense.UserID != member {
			t.Errorf("detail user_id (claimant): got %q, want member %q", resp.Expense.UserID, member)
		}
	})

	t.Run("malformed user_id is a 400", func(t *testing.T) {
		body := onBehalfBody(t, ts, "")
		body.UserID = strPtr("not-a-uuid") // fails the binding:"omitempty,uuid" tag

		rec := postExpense(t, ts, bearer(t, ts, devUserID, devOrgID), body)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("malformed user_id: expected 400, got %d — body: %s", rec.Code, rec.Body.String())
		}
	})
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

// =============================================================================
// EXPENSE CATEGORIES
// =============================================================================

// TestHandleListExpenseCategories covers GET /api/v1/expense-categories:
//   - authenticated → 200 with the org's ACTIVE categories, each carrying its
//     category_group; spot-checks one category per group and the capital-asset
//     flag; confirms a deactivated legacy category is excluded.
//   - no token → 401.
func TestHandleListExpenseCategories(t *testing.T) {
	ts := newTestServer(t)
	defer ts.pool.Close()

	t.Run("authenticated lists active grouped categories", func(t *testing.T) {
		recorder := httptest.NewRecorder()
		req, _ := http.NewRequest(http.MethodGet, "/api/v1/expense-categories", nil)
		req.Header.Set("Authorization", bearer(t, ts, devUserID, devOrgID))
		ts.server.router.ServeHTTP(recorder, req)

		if recorder.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d — body: %s", recorder.Code, recorder.Body.String())
		}

		var resp struct {
			ExpenseCategories []ExpenseCategoryResponse `json:"expense_categories"`
		}
		if err := json.Unmarshal(recorder.Body.Bytes(), &resp); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if len(resp.ExpenseCategories) == 0 {
			t.Fatal("expected a non-empty category list")
		}

		// Index by name; every active category must be grouped and identified.
		byName := make(map[string]ExpenseCategoryResponse, len(resp.ExpenseCategories))
		for _, c := range resp.ExpenseCategories {
			byName[c.Name] = c
			if c.CategoryGroup == nil || *c.CategoryGroup == "" {
				t.Errorf("category %q has no category_group", c.Name)
			}
			if c.ID == "" || c.NominalCode == "" {
				t.Errorf("category %q missing id/nominal_code", c.Name)
			}
		}

		// Spot-check one category per group, including the capital-asset flag.
		checks := []struct {
			name      string
			group     string
			isCapital bool
		}{
			{"Cost of Sales", "COS", false},
			{"Accommodation and Meals", "ADMIN", false},
			{"Computer Equipment Purchase", "ASSETS", true},
		}
		for _, ck := range checks {
			c, ok := byName[ck.name]
			if !ok {
				t.Errorf("expected category %q in the list", ck.name)
				continue
			}
			if c.CategoryGroup == nil || *c.CategoryGroup != ck.group {
				t.Errorf("%q: category_group = %v, want %q", ck.name, c.CategoryGroup, ck.group)
			}
			if c.IsCapitalAsset != ck.isCapital {
				t.Errorf("%q: is_capital_asset = %v, want %v", ck.name, c.IsCapitalAsset, ck.isCapital)
			}
		}

		// Deactivated legacy categories must NOT appear.
		if _, found := byName["Travel & Subsistence"]; found {
			t.Error("deactivated category 'Travel & Subsistence' should not be listed")
		}
	})

	t.Run("requires auth", func(t *testing.T) {
		recorder := httptest.NewRecorder()
		req, _ := http.NewRequest(http.MethodGet, "/api/v1/expense-categories", nil)
		ts.server.router.ServeHTTP(recorder, req)
		if recorder.Code != http.StatusUnauthorized {
			t.Errorf("expected 401, got %d — body: %s", recorder.Code, recorder.Body.String())
		}
	})
}

// =============================================================================
// EXPENSE UPDATE
// =============================================================================

// validUpdateBody builds a complete, valid PUT body (fresh random category +
// fields); individual subtests tweak what they care about.
func validUpdateBody(t *testing.T, ts *testServer) UpdateExpenseRequest {
	t.Helper()
	return UpdateExpenseRequest{
		CategoryID:       randomCategoryUUID(t, ts.pool),
		DatedOn:          util.RandomDatedOn(),
		Description:      util.RandomExpenseDescription(),
		CurrencyCode:     "GBP",
		GrossValuePounds: util.RandomGrossValue(),
	}
}

// putExpense sends PUT /api/v1/expenses/:id with the given auth header (empty =
// none) and JSON body, returning the recorder.
func putExpense(t *testing.T, ts *testServer, id, authHeader string, body UpdateExpenseRequest) *httptest.ResponseRecorder {
	t.Helper()
	bodyBytes, _ := json.Marshal(body)
	rec := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodPut, "/api/v1/expenses/"+id, bytes.NewReader(bodyBytes))
	req.Header.Set("Content-Type", "application/json")
	if authHeader != "" {
		req.Header.Set("Authorization", authHeader)
	}
	ts.server.router.ServeHTTP(rec, req)
	return rec
}

// TestHandleUpdateExpense covers PUT /api/v1/expenses/:id and its authorization:
// owner edits own (200, persisted), org owner/admin edits a member's (200),
// member edits another's (403), unknown id (404), no token (401), and the
// DRAFT/REJECTED status guard (409).
func TestHandleUpdateExpense(t *testing.T) {
	ts := newTestServer(t)
	defer ts.pool.Close()

	t.Run("owner edits own expense", func(t *testing.T) {
		id := createExpenseAs(t, ts, devUserID, devOrgID)

		body := validUpdateBody(t, ts)
		body.Description = "Updated " + util.RandomExpenseDescription()
		body.GrossValuePounds = "99.99"

		rec := putExpense(t, ts, id, bearer(t, ts, devUserID, devOrgID), body)
		if rec.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d — body: %s", rec.Code, rec.Body.String())
		}

		var resp struct {
			Expense ExpenseResponse `json:"expense"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if resp.Expense.Description != body.Description {
			t.Errorf("description: got %q, want %q", resp.Expense.Description, body.Description)
		}
		if resp.Expense.GrossValue != "99.99" {
			t.Errorf("gross_value: got %q, want %q", resp.Expense.GrossValue, "99.99")
		}

		// The change must be persisted in the DB.
		var dbDesc string
		var dbGrossMinor int32
		if err := ts.pool.QueryRow(context.Background(),
			"SELECT description, gross_value_minor FROM expenses WHERE id=$1", id).Scan(&dbDesc, &dbGrossMinor); err != nil {
			t.Fatalf("db read: %v", err)
		}
		if dbDesc != body.Description || dbGrossMinor != 9999 {
			t.Errorf("db row not updated: desc=%q gross_minor=%d", dbDesc, dbGrossMinor)
		}
	})

	t.Run("org owner/admin edits a member's expense", func(t *testing.T) {
		memberID := newMemberUser(t, ts, devOrgID)
		id := createExpenseAs(t, ts, memberID, devOrgID)

		rec := putExpense(t, ts, id, bearer(t, ts, devUserID, devOrgID), validUpdateBody(t, ts))
		if rec.Code != http.StatusOK {
			t.Fatalf("admin editing member's expense: expected 200, got %d — body: %s", rec.Code, rec.Body.String())
		}
	})

	t.Run("member cannot edit another user's expense", func(t *testing.T) {
		ownerExpense := createExpenseAs(t, ts, devUserID, devOrgID)
		memberID := newMemberUser(t, ts, devOrgID)

		rec := putExpense(t, ts, ownerExpense, bearer(t, ts, memberID, devOrgID), validUpdateBody(t, ts))
		if rec.Code != http.StatusForbidden {
			t.Errorf("member editing owner's expense: expected 403, got %d — body: %s", rec.Code, rec.Body.String())
		}
	})

	t.Run("unknown id returns 404", func(t *testing.T) {
		rec := putExpense(t, ts, uuid.NewString(), bearer(t, ts, devUserID, devOrgID), validUpdateBody(t, ts))
		if rec.Code != http.StatusNotFound {
			t.Errorf("expected 404, got %d — body: %s", rec.Code, rec.Body.String())
		}
	})

	t.Run("requires auth", func(t *testing.T) {
		rec := putExpense(t, ts, uuid.NewString(), "", validUpdateBody(t, ts))
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("expected 401, got %d — body: %s", rec.Code, rec.Body.String())
		}
	})

	t.Run("non-editable status is rejected", func(t *testing.T) {
		id := createExpenseAs(t, ts, devUserID, devOrgID)
		// Move it out of DRAFT so it can no longer be edited.
		if _, err := ts.pool.Exec(context.Background(),
			"UPDATE expenses SET status='APPROVED' WHERE id=$1", id); err != nil {
			t.Fatalf("set status: %v", err)
		}
		rec := putExpense(t, ts, id, bearer(t, ts, devUserID, devOrgID), validUpdateBody(t, ts))
		if rec.Code != http.StatusConflict {
			t.Errorf("expected 409, got %d — body: %s", rec.Code, rec.Body.String())
		}
	})
}

// =============================================================================
// EXPENSE DELETE
// =============================================================================

// deleteExpense sends DELETE /api/v1/expenses/:id with the given auth header
// (empty = none), returning the recorder.
func deleteExpense(t *testing.T, ts *testServer, id, authHeader string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodDelete, "/api/v1/expenses/"+id, nil)
	if authHeader != "" {
		req.Header.Set("Authorization", authHeader)
	}
	ts.server.router.ServeHTTP(rec, req)
	return rec
}

// TestHandleDeleteExpense covers DELETE /api/v1/expenses/:id: a soft-delete of a
// DRAFT/REJECTED expense, its authorization, the status guard, and the motivating
// case — removing an abandoned Smart Upload capture so it leaves the inbox.
func TestHandleDeleteExpense(t *testing.T) {
	ts := newTestServer(t)
	defer ts.pool.Close()

	t.Run("owner deletes own DRAFT → 204, soft-deleted, then 404", func(t *testing.T) {
		id := createExpenseAs(t, ts, devUserID, devOrgID)

		rec := deleteExpense(t, ts, id, bearer(t, ts, devUserID, devOrgID))
		if rec.Code != http.StatusNoContent {
			t.Fatalf("expected 204, got %d — body: %s", rec.Code, rec.Body.String())
		}

		// It's a SOFT delete: the row remains with deleted_at set.
		var deletedAt *time.Time
		if err := ts.pool.QueryRow(context.Background(),
			"SELECT deleted_at FROM expenses WHERE id=$1", id).Scan(&deletedAt); err != nil {
			t.Fatalf("read deleted_at: %v", err)
		}
		if deletedAt == nil {
			t.Error("expected deleted_at to be set after delete")
		}

		// And it's now invisible: GET → 404, absent from the list.
		getRec := httptest.NewRecorder()
		getReq, _ := http.NewRequest(http.MethodGet, "/api/v1/expenses/"+id, nil)
		getReq.Header.Set("Authorization", bearer(t, ts, devUserID, devOrgID))
		ts.server.router.ServeHTTP(getRec, getReq)
		if getRec.Code != http.StatusNotFound {
			t.Errorf("GET after delete: expected 404, got %d", getRec.Code)
		}

		listRec := httptest.NewRecorder()
		listReq, _ := http.NewRequest(http.MethodGet, "/api/v1/expenses", nil)
		listReq.Header.Set("Authorization", bearer(t, ts, devUserID, devOrgID))
		ts.server.router.ServeHTTP(listRec, listReq)
		if contains(expenseIDsFromList(t, listRec.Body.Bytes()), id) {
			t.Error("a deleted expense must not appear in the list")
		}
	})

	t.Run("org owner/admin deletes a member's draft → 204", func(t *testing.T) {
		memberID := newMemberUser(t, ts, devOrgID)
		id := createExpenseAs(t, ts, memberID, devOrgID)

		rec := deleteExpense(t, ts, id, bearer(t, ts, devUserID, devOrgID))
		if rec.Code != http.StatusNoContent {
			t.Fatalf("admin deleting member's draft: expected 204, got %d — body: %s", rec.Code, rec.Body.String())
		}
	})

	t.Run("member cannot delete another user's expense → 403", func(t *testing.T) {
		ownerExpense := createExpenseAs(t, ts, devUserID, devOrgID)
		memberID := newMemberUser(t, ts, devOrgID)

		rec := deleteExpense(t, ts, ownerExpense, bearer(t, ts, memberID, devOrgID))
		if rec.Code != http.StatusForbidden {
			t.Errorf("member deleting owner's expense: expected 403, got %d — body: %s", rec.Code, rec.Body.String())
		}
	})

	t.Run("non-deletable status is rejected → 409", func(t *testing.T) {
		id := createExpenseAs(t, ts, devUserID, devOrgID)
		// Move it out of DRAFT so it can no longer be deleted.
		if _, err := ts.pool.Exec(context.Background(),
			"UPDATE expenses SET status='APPROVED' WHERE id=$1", id); err != nil {
			t.Fatalf("set status: %v", err)
		}
		rec := deleteExpense(t, ts, id, bearer(t, ts, devUserID, devOrgID))
		if rec.Code != http.StatusConflict {
			t.Errorf("deleting an APPROVED expense: expected 409, got %d — body: %s", rec.Code, rec.Body.String())
		}
	})

	t.Run("unknown id → 404", func(t *testing.T) {
		rec := deleteExpense(t, ts, uuid.NewString(), bearer(t, ts, devUserID, devOrgID))
		if rec.Code != http.StatusNotFound {
			t.Errorf("expected 404, got %d — body: %s", rec.Code, rec.Body.String())
		}
	})

	t.Run("requires auth → 401", func(t *testing.T) {
		rec := deleteExpense(t, ts, uuid.NewString(), "")
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("expected 401, got %d — body: %s", rec.Code, rec.Body.String())
		}
	})

	t.Run("another org cannot delete this org's expense → 404", func(t *testing.T) {
		expenseA := createExpenseAs(t, ts, devUserID, devOrgID)
		orgB, userB := newOrgWithOwner(t, ts)

		// The expense isn't in org B's scope, so it's a 404 for them (existence is
		// not revealed across tenants).
		rec := deleteExpense(t, ts, expenseA, bearer(t, ts, userB, orgB))
		if rec.Code != http.StatusNotFound {
			t.Errorf("cross-tenant delete: expected 404, got %d — body: %s", rec.Code, rec.Body.String())
		}
	})

	t.Run("needs_review capture is deleted and leaves the inbox", func(t *testing.T) {
		requireGCS(t) // captureAs stores a file in the real GCS dev bucket
		draft := captureAs(t, ts, &spyEnqueuer{}, devUserID, devOrgID, DocumentTypeReceipt, "r.pdf", samplePDF())

		// captureAs's own cleanup can't reclaim the GCS object once the expense is
		// soft-deleted (DeleteAttachment won't find it), so reclaim it directly here.
		var storageKey string
		if err := ts.pool.QueryRow(context.Background(),
			"SELECT storage_path FROM expense_attachments WHERE id=$1", draft.Attachments[0].ID).Scan(&storageKey); err != nil {
			t.Fatalf("read storage_path: %v", err)
		}
		t.Cleanup(func() { _ = ts.server.attachmentService.storage.Delete(context.Background(), storageKey) })

		// It starts in the inbox.
		caller, org := mustUUID(t, devUserID), mustUUID(t, devOrgID)
		inbox, err := ts.server.expenseService.ListInbox(context.Background(), caller, org)
		if err != nil {
			t.Fatalf("ListInbox: %v", err)
		}
		if !containsExpense(inbox, draft.ID) {
			t.Fatalf("capture %s should be in the inbox before delete", draft.ID)
		}

		// Delete it via the API → 204.
		rec := deleteExpense(t, ts, draft.ID, bearer(t, ts, devUserID, devOrgID))
		if rec.Code != http.StatusNoContent {
			t.Fatalf("delete capture: expected 204, got %d — body: %s", rec.Code, rec.Body.String())
		}

		// ...and it has left the inbox.
		inboxAfter, err := ts.server.expenseService.ListInbox(context.Background(), caller, org)
		if err != nil {
			t.Fatalf("ListInbox after delete: %v", err)
		}
		if containsExpense(inboxAfter, draft.ID) {
			t.Error("a deleted capture must leave the review inbox")
		}
	})
}

// =============================================================================
// EXPENSE VAT
// =============================================================================

// postExpense sends POST /api/v1/expenses with the given auth header and body.
func postExpense(t *testing.T, ts *testServer, authHeader string, body CreateExpenseRequest) *httptest.ResponseRecorder {
	t.Helper()
	bodyBytes, _ := json.Marshal(body)
	rec := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodPost, "/api/v1/expenses", bytes.NewReader(bodyBytes))
	req.Header.Set("Content-Type", "application/json")
	if authHeader != "" {
		req.Header.Set("Authorization", authHeader)
	}
	ts.server.router.ServeHTTP(rec, req)
	return rec
}

// decodeExpense decodes a {"expense": {...}} envelope into an ExpenseResponse.
func decodeExpense(t *testing.T, rec *httptest.ResponseRecorder) ExpenseResponse {
	t.Helper()
	var resp struct {
		Expense ExpenseResponse `json:"expense"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode expense: %v — body: %s", err, rec.Body.String())
	}
	return resp.Expense
}

// gbVatRateID returns the id of a seeded GB VAT rate matching the predicate.
func gbVatRateID(t *testing.T, ts *testServer, fixed bool, rateBps int32) string {
	t.Helper()
	var id string
	err := ts.pool.QueryRow(context.Background(),
		"SELECT id::text FROM vat_rates WHERE country_code='GB' AND is_fixed_ratio=$1 AND rate_bps=$2 LIMIT 1",
		fixed, rateBps).Scan(&id)
	if err != nil {
		t.Fatalf("need a GB vat_rate (fixed=%v, bps=%d) seeded: %v", fixed, rateBps, err)
	}
	return id
}

// TestExpenseVAT covers VAT handling in create and update:
//   - fixed-ratio rate → backend computes gross × rate and IGNORES any client amount
//   - non-fixed rate   → backend stores the client-supplied amount
//   - half-up rounding on a half-penny result
//   - a rate from another country is rejected (422)
//   - update recomputes VAT for a fixed-ratio rate
func TestExpenseVAT(t *testing.T) {
	ts := newTestServer(t)
	defer ts.pool.Close()

	devAuth := bearer(t, ts, devUserID, devOrgID)
	fixedRateID := gbVatRateID(t, ts, true, 2000)     // Standard Rate 20%
	nonFixedRateID := gbVatRateID(t, ts, false, 2000) // Standard Rate (manual) 20%

	baseBody := func() CreateExpenseRequest {
		return CreateExpenseRequest{
			CategoryID:       randomCategoryUUID(t, ts.pool),
			DatedOn:          util.RandomDatedOn(),
			Description:      util.RandomExpenseDescription(),
			CurrencyCode:     "GBP",
			GrossValuePounds: "100.00",
		}
	}

	t.Run("fixed-ratio extracts VAT from inclusive total and ignores client amount", func(t *testing.T) {
		body := baseBody()
		body.GrossValuePounds = "120.00" // VAT-inclusive total (£100 net + £20 VAT)
		body.VATRateID = &fixedRateID
		bogus := "99.00"
		body.VATAmount = &bogus // must be ignored

		rec := postExpense(t, ts, devAuth, body)
		if rec.Code != http.StatusCreated {
			t.Fatalf("expected 201, got %d — body: %s", rec.Code, rec.Body.String())
		}
		got := decodeExpense(t, rec)
		if got.VATValue != "20.00" {
			t.Errorf("vat_value: got %q, want %q (£120 incl. 20%% → £20 VAT; client 99.00 ignored)", got.VATValue, "20.00")
		}
	})

	t.Run("non-fixed-ratio uses client amount", func(t *testing.T) {
		body := baseBody()
		body.VATRateID = &nonFixedRateID
		amt := "3.33"
		body.VATAmount = &amt

		rec := postExpense(t, ts, devAuth, body)
		if rec.Code != http.StatusCreated {
			t.Fatalf("expected 201, got %d — body: %s", rec.Code, rec.Body.String())
		}
		got := decodeExpense(t, rec)
		if got.VATValue != "3.33" {
			t.Errorf("vat_value: got %q, want %q (client amount on a non-fixed rate)", got.VATValue, "3.33")
		}
	})

	t.Run("fixed-ratio rounds half-up", func(t *testing.T) {
		// £0.03 incl. 20% → 3 × 2000 / 12000 = 0.5p → rounds half-up to 1p = £0.01.
		body := baseBody()
		body.GrossValuePounds = "0.03"
		body.VATRateID = &fixedRateID

		rec := postExpense(t, ts, devAuth, body)
		if rec.Code != http.StatusCreated {
			t.Fatalf("expected 201, got %d — body: %s", rec.Code, rec.Body.String())
		}
		got := decodeExpense(t, rec)
		if got.VATValue != "0.01" {
			t.Errorf("vat_value: got %q, want %q (0.5p rounds half-up to 1p)", got.VATValue, "0.01")
		}
	})

	t.Run("rate from another country is rejected", func(t *testing.T) {
		frRateID := uuid.NewString()
		if _, err := ts.pool.Exec(context.Background(),
			"INSERT INTO vat_rates (id, country_code, name, rate_bps, is_fixed_ratio, effective_from) VALUES ($1,'FR','TVA Standard',2000,true,'2000-01-01')",
			frRateID); err != nil {
			t.Fatalf("insert FR rate: %v", err)
		}
		t.Cleanup(func() {
			_, _ = ts.pool.Exec(context.Background(), "DELETE FROM vat_rates WHERE id=$1", frRateID)
		})

		body := baseBody()
		body.VATRateID = &frRateID

		rec := postExpense(t, ts, devAuth, body)
		if rec.Code != http.StatusUnprocessableEntity {
			t.Errorf("wrong-country rate: expected 422, got %d — body: %s", rec.Code, rec.Body.String())
		}
	})

	t.Run("update recomputes fixed-ratio VAT", func(t *testing.T) {
		id := createExpenseAs(t, ts, devUserID, devOrgID) // created with no VAT rate → vat 0
		body := validUpdateBody(t, ts)
		body.GrossValuePounds = "60.00" // VAT-inclusive total (£50 net + £10 VAT)
		body.VATRateID = &fixedRateID

		rec := putExpense(t, ts, id, devAuth, body)
		if rec.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d — body: %s", rec.Code, rec.Body.String())
		}
		got := decodeExpense(t, rec)
		if got.VATValue != "10.00" {
			t.Errorf("vat_value: got %q, want %q (£60 incl. 20%% → £10 VAT)", got.VATValue, "10.00")
		}
	})
}

// =============================================================================
// PASSWORD RESET
// =============================================================================

// fakeEmailSender is a test EmailSender that records the last message instead of
// sending it, so tests can pull the reset link/token back out.
type fakeEmailSender struct {
	called  bool
	to      string
	subject string
	body    string
}

func (f *fakeEmailSender) Send(_ context.Context, to, subject, body string) error {
	f.called, f.to, f.subject, f.body = true, to, subject, body
	return nil
}

// newUserWithPassword inserts an ephemeral active user (bcrypt password + an
// active membership in orgID) and registers cleanup. Returns the user's id and
// email. Keeps the seeded dev user's password untouched.
func newUserWithPassword(t *testing.T, ts *testServer, orgID, plainPassword string) (id, email string) {
	t.Helper()
	id = uuid.NewString()
	email = "reset-" + id + "@test.local"
	hash, err := bcrypt.GenerateFromPassword([]byte(plainPassword), 12)
	if err != nil {
		t.Fatalf("newUserWithPassword: hash: %v", err)
	}
	ctx := context.Background()
	if _, err := ts.pool.Exec(ctx,
		`INSERT INTO users (id, email, password_hash, first_name, last_name, is_active, email_verified_at)
		 VALUES ($1, $2, $3, 'Reset', 'Tester', TRUE, now())`, id, email, string(hash)); err != nil {
		t.Fatalf("newUserWithPassword: insert user: %v", err)
	}
	if _, err := ts.pool.Exec(ctx,
		`INSERT INTO organisation_memberships (organisation_id, user_id, role, status)
		 VALUES ($1, $2, 'member', 'active')`, orgID, id); err != nil {
		t.Fatalf("newUserWithPassword: insert membership: %v", err)
	}
	t.Cleanup(func() {
		_, _ = ts.pool.Exec(ctx, `DELETE FROM organisation_memberships WHERE user_id = $1`, id)
		_, _ = ts.pool.Exec(ctx, `DELETE FROM users WHERE id = $1`, id)
	})
	return id, email
}

// extractResetToken pulls the raw token out of the reset email body, which
// contains "{base}/reset-password/<token>" on its own line.
func extractResetToken(t *testing.T, body string) string {
	t.Helper()
	const marker = "/reset-password/"
	i := strings.Index(body, marker)
	if i < 0 {
		t.Fatalf("reset email missing %q:\n%s", marker, body)
	}
	// The token runs from just after the marker to the first whitespace (the link
	// is on its own line; base64url tokens contain no whitespace).
	fields := strings.Fields(body[i+len(marker):])
	if len(fields) == 0 {
		t.Fatalf("reset email has no token after %q:\n%s", marker, body)
	}
	return fields[0]
}

// TestPasswordReset covers the forgot-password → reset-password flow:
// happy path (request → reset → login with new password, old rejected, token
// single-use), unknown email (200, nothing sent), expired token (400), and a
// garbage token (400).
func TestPasswordReset(t *testing.T) {
	ts := newTestServer(t)
	defer ts.pool.Close()

	const oldPassword = "oldpassword123"
	const newPassword = "newpassword456"

	postJSON := func(path string, body any) *httptest.ResponseRecorder {
		b, _ := json.Marshal(body)
		rec := httptest.NewRecorder()
		req, _ := http.NewRequest(http.MethodPost, path, bytes.NewReader(b))
		req.Header.Set("Content-Type", "application/json")
		ts.server.router.ServeHTTP(rec, req)
		return rec
	}
	login := func(email, password string) int {
		return postJSON("/api/v1/auth/login", map[string]string{"email": email, "password": password}).Code
	}

	t.Run("happy path: request, reset, login with new password", func(t *testing.T) {
		_, email := newUserWithPassword(t, ts, devOrgID, oldPassword)

		// 1) Request a reset → always 200; the fake sender captured the link.
		ts.emailSender.called = false
		if rec := postJSON("/api/v1/auth/forgot-password", map[string]string{"email": email}); rec.Code != http.StatusOK {
			t.Fatalf("forgot-password: expected 200, got %d — body: %s", rec.Code, rec.Body.String())
		}
		if !ts.emailSender.called {
			t.Fatal("expected a reset email to be sent")
		}
		token := extractResetToken(t, ts.emailSender.body)

		// 2) Reset with the token → 200.
		if rec := postJSON("/api/v1/auth/reset-password/"+token, map[string]string{"password": newPassword}); rec.Code != http.StatusOK {
			t.Fatalf("reset-password: expected 200, got %d — body: %s", rec.Code, rec.Body.String())
		}

		// 3) New password logs in; the old password is now rejected.
		if code := login(email, newPassword); code != http.StatusOK {
			t.Errorf("login with new password: expected 200, got %d", code)
		}
		if code := login(email, oldPassword); code != http.StatusUnauthorized {
			t.Errorf("login with old password: expected 401, got %d", code)
		}

		// 4) The token is single-use — reusing it fails.
		if rec := postJSON("/api/v1/auth/reset-password/"+token, map[string]string{"password": "anotherpass789"}); rec.Code != http.StatusBadRequest {
			t.Errorf("reused token: expected 400, got %d — body: %s", rec.Code, rec.Body.String())
		}
	})

	t.Run("unknown email still returns 200 and sends nothing", func(t *testing.T) {
		ts.emailSender.called = false
		rec := postJSON("/api/v1/auth/forgot-password", map[string]string{"email": "nobody-" + uuid.NewString() + "@test.local"})
		if rec.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d — body: %s", rec.Code, rec.Body.String())
		}
		if ts.emailSender.called {
			t.Error("no email should be sent for an unknown address")
		}
	})

	t.Run("expired token is rejected", func(t *testing.T) {
		_, email := newUserWithPassword(t, ts, devOrgID, oldPassword)
		ts.emailSender.called = false
		if rec := postJSON("/api/v1/auth/forgot-password", map[string]string{"email": email}); rec.Code != http.StatusOK {
			t.Fatalf("forgot-password: expected 200, got %d", rec.Code)
		}
		token := extractResetToken(t, ts.emailSender.body)

		// Backdate the sent time beyond the 15-minute TTL.
		if _, err := ts.pool.Exec(context.Background(),
			"UPDATE users SET password_reset_sent_at = now() - interval '20 minutes' WHERE email = $1", email); err != nil {
			t.Fatalf("backdate sent_at: %v", err)
		}
		if rec := postJSON("/api/v1/auth/reset-password/"+token, map[string]string{"password": newPassword}); rec.Code != http.StatusBadRequest {
			t.Errorf("expired token: expected 400, got %d — body: %s", rec.Code, rec.Body.String())
		}
	})

	t.Run("garbage token is rejected", func(t *testing.T) {
		rec := postJSON("/api/v1/auth/reset-password/not-a-real-token", map[string]string{"password": newPassword})
		if rec.Code != http.StatusBadRequest {
			t.Errorf("garbage token: expected 400, got %d — body: %s", rec.Code, rec.Body.String())
		}
	})
}

// =============================================================================
// EXPENSE CSV EXPORT — GET /api/v1/expenses/export
// =============================================================================

// Column positions of the export CSV (must match expenseExportHeader in
// expense_service.go). Named so the assertions read clearly.
const (
	colClaimantEmail = 0
	colCategory      = 1
	colDate          = 2
	colCurrency      = 3
	colGrossValue    = 4
	colDescription   = 5
	colSupplierName  = 6
	colReceiptRef    = 7
	colInvoiceNumber = 8
	colSalesTaxRate  = 9
	colSalesTaxValue = 10
	colECStatus      = 11
)

// createExpenseWith POSTs an expense with caller-chosen fields and returns its id,
// registering a best-effort cleanup that deletes the row by id. Unlike
// createExpenseAs it pins the category / date / description / gross (and an optional
// VAT rate) so the export's cells can be asserted exactly. datedOn is YYYY-MM-DD.
func createExpenseWith(t *testing.T, ts *testServer, userID, orgID, categoryID, datedOn, description, gross, vatRateID string) string {
	t.Helper()
	reqBody := CreateExpenseRequest{
		CategoryID:       categoryID,
		DatedOn:          datedOn,
		Description:      description,
		CurrencyCode:     "GBP",
		GrossValuePounds: gross,
	}
	if vatRateID != "" {
		reqBody.VATRateID = &vatRateID
	}
	bodyBytes, _ := json.Marshal(reqBody)
	recorder := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodPost, "/api/v1/expenses", bytes.NewReader(bodyBytes))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", bearer(t, ts, userID, orgID))
	ts.server.router.ServeHTTP(recorder, req)
	if recorder.Code != http.StatusCreated {
		t.Fatalf("createExpenseWith: expected 201, got %d — body: %s", recorder.Code, recorder.Body.String())
	}
	var resp map[string]ExpenseResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &resp); err != nil {
		t.Fatalf("createExpenseWith: decode: %v", err)
	}
	id := resp["expense"].ID
	t.Cleanup(func() { _, _ = ts.pool.Exec(context.Background(), `DELETE FROM expenses WHERE id = $1`, id) })
	return id
}

// exportCSV POSTs the export with NO body (so the backend exports everything the
// caller may see) and returns the parsed records, header at index 0.
func exportCSV(t *testing.T, ts *testServer, userID, orgID string) [][]string {
	t.Helper()
	recorder := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodPost, "/api/v1/expenses/export", nil)
	req.Header.Set("Authorization", bearer(t, ts, userID, orgID))
	ts.server.router.ServeHTTP(recorder, req)
	return parseExportCSV(t, recorder)
}

// exportCSVIDs POSTs {"ids": ids} so the backend exports exactly those rows — the
// SPA's "export the filtered list" path. An empty slice is sent as [], distinct
// from exportCSV's no-body call.
func exportCSVIDs(t *testing.T, ts *testServer, userID, orgID string, ids []string) [][]string {
	t.Helper()
	body, _ := json.Marshal(map[string][]string{"ids": ids})
	recorder := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodPost, "/api/v1/expenses/export", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", bearer(t, ts, userID, orgID))
	ts.server.router.ServeHTTP(recorder, req)
	return parseExportCSV(t, recorder)
}

// parseExportCSV asserts 200 + text/csv on an export response and returns the
// parsed records (header at index 0).
func parseExportCSV(t *testing.T, recorder *httptest.ResponseRecorder) [][]string {
	t.Helper()
	if recorder.Code != http.StatusOK {
		t.Fatalf("export: expected 200, got %d — body: %s", recorder.Code, recorder.Body.String())
	}
	if ct := recorder.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/csv") {
		t.Errorf("export: Content-Type = %q, want text/csv", ct)
	}
	records, err := csv.NewReader(recorder.Body).ReadAll()
	if err != nil {
		t.Fatalf("export: parse CSV: %v", err)
	}
	if len(records) == 0 {
		t.Fatal("export: no records (missing header row)")
	}
	return records
}

// assertHeader checks the first record is exactly expenseExportHeader.
func assertHeader(t *testing.T, records [][]string) {
	t.Helper()
	got := records[0]
	if len(got) != len(expenseExportHeader) {
		t.Fatalf("header has %d columns, want %d: %v", len(got), len(expenseExportHeader), got)
	}
	for i, h := range expenseExportHeader {
		if got[i] != h {
			t.Errorf("header[%d] = %q, want %q", i, got[i], h)
		}
	}
}

// findExportRow returns the first data record whose description column equals
// want, or nil. (The export has no id column, so tests match on a unique
// description they set on creation.)
func findExportRow(records [][]string, want string) []string {
	for _, rec := range records[1:] { // skip header
		if len(rec) > colDescription && rec[colDescription] == want {
			return rec
		}
	}
	return nil
}

// userEmail reads a user's login email straight from the DB (the export resolves
// claimant_email from the same source).
func userEmail(t *testing.T, ts *testServer, userID string) string {
	t.Helper()
	var email string
	if err := ts.pool.QueryRow(context.Background(), `SELECT email FROM users WHERE id = $1`, userID).Scan(&email); err != nil {
		t.Fatalf("userEmail: %v", err)
	}
	return email
}

// namedCategory returns an ordinary (non-asset/mileage/stock) active category's
// id and name for the org, so the export's category cell can be asserted.
func namedCategory(t *testing.T, ts *testServer, orgID string) (id, name string) {
	t.Helper()
	err := ts.pool.QueryRow(context.Background(), `
		SELECT id::text, name FROM expense_categories
		WHERE organisation_id = $1 AND is_active = TRUE
		  AND is_capital_asset = FALSE AND is_mileage = FALSE AND is_stock_purchase = FALSE
		ORDER BY nominal_code LIMIT 1`, orgID).Scan(&id, &name)
	if err != nil {
		t.Fatalf("namedCategory: %v", err)
	}
	return id, name
}

// gbStandardVatRateID returns the id of a seeded GB 20% fixed-ratio VAT rate, or
// "" if the vat_rates seed isn't present — the VAT assertions then fall back to
// the no-VAT shape so the test stays meaningful either way.
func gbStandardVatRateID(t *testing.T, ts *testServer) string {
	t.Helper()
	var id string
	err := ts.pool.QueryRow(context.Background(), `
		SELECT id::text FROM vat_rates
		WHERE country_code = 'GB' AND rate_bps = 2000 AND is_fixed_ratio = TRUE
		LIMIT 1`).Scan(&id)
	if err != nil {
		return ""
	}
	return id
}

// newCategory inserts an ephemeral active expense category for an org and returns
// its id, with cleanup. Used to give a second tenant a category to file against.
// (newOrgWithOwner — the second-tenant helper — lives in attachment_service_test.go.)
func newCategory(t *testing.T, ts *testServer, orgID string) string {
	t.Helper()
	ctx := context.Background()
	id := uuid.NewString()
	if _, err := ts.pool.Exec(ctx,
		`INSERT INTO expense_categories (id, organisation_id, nominal_code, name)
		 VALUES ($1, $2, '7999', 'Export Test Category')`, id, orgID); err != nil {
		t.Fatalf("newCategory: %v", err)
	}
	t.Cleanup(func() { _, _ = ts.pool.Exec(ctx, `DELETE FROM expense_categories WHERE id = $1`, id) })
	return id
}

// TestExportExpenses_OwnerAllMemberOwn verifies the export honours the same
// visibility rule as the list: an owner exports the whole org (and each row shows
// the right claimant email), a plain member exports only their own.
func TestExportExpenses_OwnerAllMemberOwn(t *testing.T) {
	ts := newTestServer(t)
	defer ts.pool.Close()

	cat := randomCategoryUUID(t, ts.pool)
	ownerDesc := "EXPORT-OWNER-" + uuid.NewString()
	memberDesc := "EXPORT-MEMBER-" + uuid.NewString()

	createExpenseWith(t, ts, devUserID, devOrgID, cat, "2026-06-17", ownerDesc, "42.50", "")
	memberID := newMemberUser(t, ts, devOrgID)
	createExpenseWith(t, ts, memberID, devOrgID, cat, "2026-06-17", memberDesc, "9.99", "")

	// Owner exports → sees BOTH; the member's row carries the member's email.
	ownerRecs := exportCSV(t, ts, devUserID, devOrgID)
	assertHeader(t, ownerRecs)
	ownerRow := findExportRow(ownerRecs, ownerDesc)
	memberRow := findExportRow(ownerRecs, memberDesc)
	if ownerRow == nil || memberRow == nil {
		t.Fatalf("owner export should contain both expenses (owner=%v member=%v)", ownerRow != nil, memberRow != nil)
	}
	if got, want := memberRow[colClaimantEmail], userEmail(t, ts, memberID); got != want {
		t.Errorf("member row claimant_email = %q, want %q", got, want)
	}
	if got, want := ownerRow[colClaimantEmail], userEmail(t, ts, devUserID); got != want {
		t.Errorf("owner row claimant_email = %q, want %q", got, want)
	}

	// Member exports → only their own.
	memberRecs := exportCSV(t, ts, memberID, devOrgID)
	if findExportRow(memberRecs, memberDesc) == nil {
		t.Errorf("member export should contain the member's own expense")
	}
	if findExportRow(memberRecs, ownerDesc) != nil {
		t.Errorf("member export must NOT contain the owner's expense")
	}
}

// TestExportExpenses_Formatting checks every cell's external shape: claimant
// email, category name, DD/MM/YYYY date, currency, pounds money, default EC
// status, and (when the VAT seed is present) the VAT rate/value columns.
func TestExportExpenses_Formatting(t *testing.T) {
	ts := newTestServer(t)
	defer ts.pool.Close()

	catID, catName := namedCategory(t, ts, devOrgID)
	desc := "EXPORT-FMT-" + uuid.NewString()
	vatID := gbStandardVatRateID(t, ts) // "" → create without VAT

	createExpenseWith(t, ts, devUserID, devOrgID, catID, "2026-06-17", desc, "120.00", vatID)

	recs := exportCSV(t, ts, devUserID, devOrgID)
	assertHeader(t, recs)
	row := findExportRow(recs, desc)
	if row == nil {
		t.Fatalf("formatting export should contain the created expense %q", desc)
	}

	checks := []struct{ name, got, want string }{
		{"claimant_email", row[colClaimantEmail], userEmail(t, ts, devUserID)},
		{"category", row[colCategory], catName},
		{"date", row[colDate], "17/06/2026"}, // DD/MM/YYYY, not the API's YYYY-MM-DD
		{"currency", row[colCurrency], "GBP"},
		{"gross_value", row[colGrossValue], "120.00"},
		{"ec_status", row[colECStatus], "UK_NON_EC"},
	}
	for _, c := range checks {
		if c.got != c.want {
			t.Errorf("%s cell = %q, want %q", c.name, c.got, c.want)
		}
	}

	if vatID != "" {
		// £120 incl. 20% VAT → rate "20" (bare percent), value "20.00" (pounds).
		if got := row[colSalesTaxRate]; got != "20" {
			t.Errorf("sales_tax_rate = %q, want \"20\"", got)
		}
		if got := row[colSalesTaxValue]; got != "20.00" {
			t.Errorf("sales_tax_value = %q, want \"20.00\"", got)
		}
	} else {
		if got := row[colSalesTaxRate]; got != "" {
			t.Errorf("sales_tax_rate = %q, want empty (no VAT)", got)
		}
		if got := row[colSalesTaxValue]; got != "0.00" {
			t.Errorf("sales_tax_value = %q, want \"0.00\" (no VAT)", got)
		}
	}
}

// TestExportExpenses_MultiTenantIsolation verifies one org's export never leaks
// another org's expenses (the export query is organisation-scoped).
func TestExportExpenses_MultiTenantIsolation(t *testing.T) {
	ts := newTestServer(t)
	defer ts.pool.Close()

	otherOrg, otherOwner := newOrgWithOwner(t, ts)
	otherCat := newCategory(t, ts, otherOrg)
	otherDesc := "EXPORT-OTHERORG-" + uuid.NewString()
	createExpenseWith(t, ts, otherOwner, otherOrg, otherCat, "2026-06-17", otherDesc, "5.00", "")

	devRecs := exportCSV(t, ts, devUserID, devOrgID)
	if findExportRow(devRecs, otherDesc) != nil {
		t.Errorf("dev org export leaked another org's expense %q", otherDesc)
	}

	otherRecs := exportCSV(t, ts, otherOwner, otherOrg)
	if findExportRow(otherRecs, otherDesc) == nil {
		t.Errorf("other org export should contain its own expense %q", otherDesc)
	}
}

// TestExportExpenses_CSVInjectionGuard verifies a free-text cell that begins with
// a formula trigger (=) is prefixed with a single quote so a spreadsheet won't
// execute it.
func TestExportExpenses_CSVInjectionGuard(t *testing.T) {
	ts := newTestServer(t)
	defer ts.pool.Close()

	cat := randomCategoryUUID(t, ts.pool)
	desc := "=SUM(A1:A9)+" + uuid.NewString() // leading '=' would be a formula
	createExpenseWith(t, ts, devUserID, devOrgID, cat, "2026-06-17", desc, "1.00", "")

	recs := exportCSV(t, ts, devUserID, devOrgID)
	guarded := "'" + desc // the export prefixes a quote
	var found bool
	for _, rec := range recs[1:] {
		if len(rec) > colDescription && rec[colDescription] == guarded {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("export should neutralise the formula description as %q", guarded)
	}
}

// TestExportExpenses_FilteredByIDs verifies that POSTing a list of ids exports
// exactly those rows (the SPA's "export only the filtered list" path) and nothing
// else — even for an admin who could otherwise see the whole org.
func TestExportExpenses_FilteredByIDs(t *testing.T) {
	ts := newTestServer(t)
	defer ts.pool.Close()

	cat := randomCategoryUUID(t, ts.pool)
	descA := "EXPORT-IDS-A-" + uuid.NewString()
	descB := "EXPORT-IDS-B-" + uuid.NewString()
	idA := createExpenseWith(t, ts, devUserID, devOrgID, cat, "2026-06-17", descA, "10.00", "")
	createExpenseWith(t, ts, devUserID, devOrgID, cat, "2026-06-17", descB, "20.00", "")

	recs := exportCSVIDs(t, ts, devUserID, devOrgID, []string{idA})
	assertHeader(t, recs)
	if findExportRow(recs, descA) == nil {
		t.Errorf("export should contain the requested expense %q", descA)
	}
	if findExportRow(recs, descB) != nil {
		t.Errorf("export must NOT contain the unrequested expense %q", descB)
	}
	if n := len(recs) - 1; n != 1 {
		t.Errorf("export should have exactly 1 data row, got %d", n)
	}
}

// TestExportExpenses_EmptyIDsExportsNothing verifies that an explicit empty id
// list (filters matched no rows) yields a header-only CSV — distinct from a
// no-body export, which means "everything".
func TestExportExpenses_EmptyIDsExportsNothing(t *testing.T) {
	ts := newTestServer(t)
	defer ts.pool.Close()

	recs := exportCSVIDs(t, ts, devUserID, devOrgID, []string{})
	assertHeader(t, recs)
	if n := len(recs) - 1; n != 0 {
		t.Errorf("empty ids should export the header only, got %d data rows", n)
	}
}

// TestExportExpenses_ByIDsOwnershipAndTenantScoped verifies the id-driven export
// can't widen what a caller sees: a member who lists their own id plus the owner's
// id plus another org's id gets back only their own row (the owner's is dropped by
// the member-sees-own rule, the other org's by org scoping).
func TestExportExpenses_ByIDsOwnershipAndTenantScoped(t *testing.T) {
	ts := newTestServer(t)
	defer ts.pool.Close()

	cat := randomCategoryUUID(t, ts.pool)
	ownerDesc := "EXPORT-IDS-OWNER-" + uuid.NewString()
	memberDesc := "EXPORT-IDS-MEMBER-" + uuid.NewString()
	ownerExp := createExpenseWith(t, ts, devUserID, devOrgID, cat, "2026-06-17", ownerDesc, "5.00", "")
	memberID := newMemberUser(t, ts, devOrgID)
	memberExp := createExpenseWith(t, ts, memberID, devOrgID, cat, "2026-06-17", memberDesc, "6.00", "")

	otherOrg, otherOwner := newOrgWithOwner(t, ts)
	otherCat := newCategory(t, ts, otherOrg)
	otherDesc := "EXPORT-IDS-OTHER-" + uuid.NewString()
	otherExp := createExpenseWith(t, ts, otherOwner, otherOrg, otherCat, "2026-06-17", otherDesc, "7.00", "")

	// The member asks for their own + the owner's + another org's ids.
	recs := exportCSVIDs(t, ts, memberID, devOrgID, []string{memberExp, ownerExp, otherExp})
	if findExportRow(recs, memberDesc) == nil {
		t.Errorf("member should export their own row %q", memberDesc)
	}
	if findExportRow(recs, ownerDesc) != nil {
		t.Errorf("member must NOT export the owner's row %q (member-sees-own)", ownerDesc)
	}
	if findExportRow(recs, otherDesc) != nil {
		t.Errorf("member must NOT export another org's row %q (tenant isolation)", otherDesc)
	}
	if n := len(recs) - 1; n != 1 {
		t.Errorf("member id-export should have exactly 1 data row, got %d", n)
	}
}
