package main

// server.go
// =============================================================================
// HTTP server: Gin engine setup, route registration, and handler methods.
//
// Responsibilities of this file:
//   - Create and configure the Gin engine
//   - Register all routes and map them to handler methods
//   - Define handler methods (the HTTP boundary — parse request, call service,
//     write response)
//
// What does NOT belong here:
//   - Business logic (that lives in expense_service.go)
//   - Database queries (that lives in db/expenses/query.sql.go)
//
// The handler's job is narrow:
//   1. Parse and validate the HTTP request
//   2. Call the service
//   3. Write the HTTP response
// =============================================================================

import (
	"encoding/csv"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/gin-contrib/cors"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/operationfb/accounting-saas/token"
)

// Server holds the Gin engine and all the services it needs to handle requests.
// Adding a new service module (invoices, contacts, etc.) means adding a field
// here and passing it into NewServer.
type Server struct {
	router            *gin.Engine
	expenseService    *ExpenseService
	attachmentService *AttachmentService
	userService       *UserService
	emailInboxService *EmailInboxService
	authHandler       *AuthHandler
	tokenMaker        token.Maker

	// mailgunSigningKey authenticates the inbound-email webhook (HMAC). Empty
	// when the channel isn't configured, in which case the webhook isn't mounted.
	mailgunSigningKey string
}

// NewServer constructs the Server, registers all routes, and returns it.
// main.go calls this once at startup.
func NewServer(expenseService *ExpenseService, attachmentService *AttachmentService, userService *UserService, emailInboxService *EmailInboxService, authHandler *AuthHandler, tokenMaker token.Maker, mailgunSigningKey string, corsOrigins []string) *Server {
	s := &Server{
		expenseService:    expenseService,
		attachmentService: attachmentService,
		userService:       userService,
		emailInboxService: emailInboxService,
		authHandler:       authHandler,
		tokenMaker:        tokenMaker,
		mailgunSigningKey: mailgunSigningKey,
	}

	// gin.Default() creates a Gin engine with two built-in middleware:
	//   - Logger: prints each request (method, path, status, latency) to stdout
	//   - Recovery: catches panics and returns 500 instead of crashing the server
	// For production you'd replace these with structured logging middleware,
	// but Default() is the right starting point.
	s.router = gin.Default()

	// How much of a multipart upload Gin buffers in memory before spilling the
	// rest to a temp file. We stream uploads to GCS and hard-cap the body in the
	// handler, so a modest in-memory buffer is plenty.
	s.router.MaxMultipartMemory = 8 << 20 // 8 MiB

	// CORS must be registered globally and BEFORE the routes/auth middleware.
	// A browser sends a preflight OPTIONS request (with no Authorization header)
	// before any cross-origin call that carries the bearer token; CORS has to
	// answer that preflight (204) before authMiddleware can reject it for the
	// missing token. Registering here (before registerRoutes) also puts CORS in
	// Gin's NoRoute/NoMethod chains so bare OPTIONS preflights are handled.
	s.router.Use(cors.New(cors.Config{
		AllowOrigins:     corsOrigins,
		AllowMethods:     []string{"GET", "POST", "PUT", "PATCH", "DELETE", "OPTIONS"},
		AllowHeaders:     []string{"Origin", "Content-Type", "Authorization", "Accept"},
		ExposeHeaders:    []string{"Content-Length"},
		AllowCredentials: false, // Bearer-token auth only; no cookies. Keep false.
		MaxAge:           12 * time.Hour,
	}))

	s.registerRoutes()

	return s
}

// Run starts the HTTP server on the given address (e.g. ":8080").
// It blocks until the server stops.
func (s *Server) Run(addr string) error {
	return s.router.Run(addr)
}

// Router exposes the underlying gin engine so domain packages (e.g.
// internal/integrations) can register their OWN routes on the same engine from
// main, AFTER NewServer has applied the global middleware (CORS). This is the
// per-domain RegisterRoutes pattern: the god Server doesn't grow per integration.
func (s *Server) Router() *gin.Engine { return s.router }

// enableStaticSPA serves the built Vue SPA (the contents of web/dist, copied into
// the container image at distDir) from the SAME origin as the API. It is wired up
// from main.go ONLY when WEB_DIST_DIR is set, so local dev and the integration
// tests — which leave it unset — are completely unaffected and keep using the Vite
// dev server.
//
// The rule is simple because every API route lives under /api/v1 (see
// registerRoutes): an unmatched /api/ path stays a JSON 404, and everything else
// falls back to index.html so the history-mode client router can resolve it.
func (s *Server) enableStaticSPA(distDir string) {
	indexFile := filepath.Join(distDir, "index.html")

	// NoRoute runs for any request that matched no registered route. The global
	// middleware (incl. CORS) still runs first, and OPTIONS preflights are already
	// short-circuited by the CORS middleware, so this only sees real misses.
	s.router.NoRoute(func(c *gin.Context) {
		p := c.Request.URL.Path

		// Never let the SPA shadow the API: an unknown /api/ path must stay a JSON
		// 404 in the standard envelope, not get the HTML index served back.
		if strings.HasPrefix(p, "/api/") {
			respondError(c, &AppError{Code: ErrCodeNotFound, Message: "resource not found"})
			return
		}

		// The SPA is a GET/HEAD surface; any other method on an unknown path is a 404.
		if c.Request.Method != http.MethodGet && c.Request.Method != http.MethodHead {
			respondError(c, &AppError{Code: ErrCodeNotFound, Message: "resource not found"})
			return
		}

		// Serve a real built asset when the path maps to an existing file inside
		// distDir (the hashed JS/CSS bundles, favicon, etc.). filepath.Clean plus the
		// distDir-prefix check guard against path traversal (e.g. "/../../etc/passwd").
		clean := filepath.Clean(p)
		target := filepath.Join(distDir, clean)
		if clean != "/" && strings.HasPrefix(target, distDir+string(os.PathSeparator)) {
			if info, err := os.Stat(target); err == nil && !info.IsDir() {
				c.File(target)
				return
			}
		}

		// Otherwise hand back index.html so the Vue history-mode router resolves the
		// client-side route (/login, /expenses, …) on the front end.
		c.File(indexFile)
	})
}

// registerRoutes declares every URL pattern and which handler method responds.
// Keeping all routes in one place makes it easy to see the full API surface.
func (s *Server) registerRoutes() {
	// Route groups let you share a URL prefix and (later) middleware.
	// All expense routes live under /api/v1/expenses.
	// Versioning (/v1/) in the URL means you can introduce /v2/ later
	// without breaking existing clients.

	// Liveness probe for Cloud Run (and uptime checks). Public, no auth, and it
	// deliberately does NOT touch the database: the startup DB ping (main.go) is the
	// real readiness gate, and we don't want a transient DB blip to fail liveness and
	// trigger pointless container restarts.
	//
	// The path is /health, NOT /healthz: Google Cloud RESERVES "/healthz" at the
	// front-end layer, so on Cloud Run a request to it never reaches the container —
	// it gets a Google 404. /health passes through normally.
	s.router.GET("/health", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"status": "ok"})
	})

	v1 := s.router.Group("/api/v1")
	{
		// Authentication routes. These are deliberately NOT behind auth
		// middleware — login is how a client obtains its token in the first place.
		authRoutes := v1.Group("/auth")
		{
			// POST /api/v1/auth/login → verify credentials, return a PASETO token
			authRoutes.POST("/login", s.authHandler.LoginUser)
			// POST /api/v1/auth/forgot-password → email a reset link (always 200, no enumeration)
			authRoutes.POST("/forgot-password", s.authHandler.ForgotPassword)
			// POST /api/v1/auth/reset-password/:token → set a new password via the emailed code
			authRoutes.POST("/reset-password/:token", s.authHandler.ResetPassword)
		}

		// Expense routes require a valid login. authMiddleware verifies the
		// bearer token and puts the user id + organisation id in the context.
		expenses := v1.Group("/expenses")
		expenses.Use(authMiddleware(s.tokenMaker))
		{
			// GET    /api/v1/expenses          → list expenses the caller may see
			// POST   /api/v1/expenses          → create a new expense (for the caller)
			// POST   /api/v1/expenses/capture  → "Smart Upload": create a draft from a
			//                                     receipt + run OCR (multipart)
			// GET    /api/v1/expenses/inbox    → captures awaiting review (needs_review)
			// POST   /api/v1/expenses/export   → download expenses as CSV (optional {ids} body)
			// GET    /api/v1/expenses/:id      → fetch one expense by UUID
			// PUT    /api/v1/expenses/:id      → update an editable (DRAFT/REJECTED) expense
			// DELETE /api/v1/expenses/:id      → soft-delete an editable (DRAFT/REJECTED) expense
			// POST   /api/v1/expenses/:id/status → run an approval-workflow transition
			expenses.GET("", s.handleListExpenses)
			expenses.POST("", s.handleCreateExpense)
			// Static segments registered before the /:id param routes. Gin matches a
			// literal "capture"/"inbox" ahead of the :id wildcard.
			expenses.POST("/capture", s.handleSmartUpload)
			expenses.GET("/inbox", s.handleListInbox)
			expenses.POST("/export", s.handleExportExpenses)
			expenses.GET("/:id", s.handleGetExpense)
			expenses.PUT("/:id", s.handleUpdateExpense)
			expenses.DELETE("/:id", s.handleDeleteExpense)
			expenses.POST("/:id/status", s.handleChangeExpenseStatus)

			// Attachments (receipt files) are a sub-resource of an expense. They
			// reuse the :id param for the expense — introducing a differently
			// named wildcard at that path position would make Gin panic.
			// POST   /:id/attachments                         → upload a file
			// GET    /:id/attachments                         → list metadata
			// GET    /:id/attachments/:attachmentId/download  → signed download URL
			// PATCH  /:id/attachments/:attachmentId/primary   → mark as primary
			// DELETE /:id/attachments/:attachmentId           → delete a file
			expenses.POST("/:id/attachments", s.handleUploadAttachment)
			expenses.GET("/:id/attachments", s.handleListAttachments)
			expenses.GET("/:id/attachments/:attachmentId/download", s.handleDownloadAttachment)
			expenses.PATCH("/:id/attachments/:attachmentId/primary", s.handleSetPrimaryAttachment)
			expenses.DELETE("/:id/attachments/:attachmentId", s.handleDeleteAttachment)
		}

		// Expense categories (reference data) — also require a valid login.
		expenseCategories := v1.Group("/expense-categories")
		expenseCategories.Use(authMiddleware(s.tokenMaker))
		{
			// GET /api/v1/expense-categories → active categories for the caller's org
			expenseCategories.GET("", s.handleListExpenseCategories)
		}

		// VAT rates (reference data) — also require a valid login. The country is
		// resolved from the caller's organisation, so there is no path/query param.
		vatRates := v1.Group("/vat-rates")
		vatRates.Use(authMiddleware(s.tokenMaker))
		{
			// GET /api/v1/vat-rates → VAT rates valid today for the caller's org country
			vatRates.GET("", s.handleListVATRates)
		}

		// Profile routes — the caller's own "My Details" (first/last name + the
		// read-only login email). Like organisation, this is a singleton resource:
		// the user is taken from the bearer token, so there's no id in the path and
		// a caller can only ever read/edit themselves.
		profile := v1.Group("/profile")
		profile.Use(authMiddleware(s.tokenMaker))
		{
			// GET /api/v1/profile → the caller's own profile
			// PUT /api/v1/profile → update the caller's first/last name
			profile.GET("", s.handleGetProfile)
			profile.PUT("", s.handleUpdateProfile)
		}

		// NOTE: the integration routes (/api/v1/integrations/{provider},
		// /api/v1/{provider}/{connect,callback}) and the /internal/v1 endpoints are
		// registered by internal/integrations' Handler.RegisterRoutes /
		// RegisterInternalRoutes, called from main on Server.Router() — so adding a
		// provider never touches this file.

		// Email-to-expense (Mailgun inbound). The webhook is PUBLIC — it carries
		// no bearer token and is authenticated by Mailgun's HMAC signature in the
		// handler — so it's mounted only when the channel is fully configured.
		if s.emailInboxService != nil && s.mailgunSigningKey != "" {
			webhooks := v1.Group("/webhooks")
			{
				// POST /api/v1/webhooks/mailgun/inbound → one parsed inbound email
				webhooks.POST("/mailgun/inbound", s.handleMailgunInbound)
			}
		}

		// The receipt-inbox address display is a normal authed route. The handler
		// reports enabled:false when the channel is off, so it's always safe to mount.
		inboxAddress := v1.Group("/inbox-address")
		inboxAddress.Use(authMiddleware(s.tokenMaker))
		{
			// GET /api/v1/inbox-address → the caller's receipt-forwarding address
			inboxAddress.GET("", s.handleGetInboxAddress)
		}
	}
}

// =============================================================================
// REQUEST / RESPONSE TYPES
//
// These structs define the shape of JSON that the API accepts and returns.
// They are deliberately separate from the database model structs (generated
// by sqlc). This separation matters because:
//
//   - The database model may have internal fields you don't want to expose
//     (e.g. deleted_at, ocr_raw_text)
//   - The API shape may differ from the DB shape (e.g. amounts in pounds
//     not pence, combined fields, computed fields)
//   - Validating at the API boundary is cleaner than validating on DB structs
//
// Struct tags explained:
//   `json:"field_name"`           — the JSON key name
//   `binding:"required"`          — Gin's validator: returns 400 if missing
//   `binding:"required,min=1"`    — required AND at least 1 character long
//   `binding:"omitempty,len=3"`   — optional, but if present must be 3 chars
// =============================================================================

// CreateExpenseRequest is the JSON body accepted by POST /api/v1/expenses.
// Only the fields a client should supply are here. Internal fields (id,
// created_at, status, etc.) are set by the service, not the client.
type CreateExpenseRequest struct {
	CategoryID       string `json:"category_id"      binding:"required,uuid"`
	DatedOn          string `json:"dated_on"          binding:"required"` // YYYY-MM-DD
	Description      string `json:"description"       binding:"required,min=1"`
	CurrencyCode     string `json:"currency"          binding:"omitempty,len=3"` // defaults to GBP
	GrossValuePounds string `json:"gross_value"     binding:"required"`          // e.g. "42.50"

	// Optional fields — pointer types so we can distinguish "not provided"
	// from "provided as empty string / zero". A nil pointer means absent.
	ReceiptReference *string `json:"receipt_reference"`
	SupplierName     *string `json:"supplier_name"`
	SupplierVATNo    *string `json:"supplier_vat_number"`
	InvoiceNumber    *string `json:"invoice_number"`

	// Optional claimant. When an owner/admin records an expense on behalf of
	// someone else, this is that person's user id. Absent (nil) → the expense is
	// for the caller (the normal case). Authorised + validated in the service.
	UserID *string `json:"user_id" binding:"omitempty,uuid"`

	// VAT
	VATRateID *string `json:"vat_rate_id"` // UUID of the applicable VAT rate
	VATAmount *string `json:"vat_amount"`  // pounds, e.g. "3.33"; used only for non-fixed-ratio rates (ignored for fixed-ratio)

	// Project rebilling — all three must be provided together if rebilling
	ProjectID    *string `json:"project_id"`
	RebillType   *string `json:"rebill_type"`   // "cost" | "markup" | "price"
	RebillFactor *string `json:"rebill_factor"` // decimal string e.g. "1.15"
}

// UpdateExpenseRequest is the JSON body accepted by PUT /api/v1/expenses/:id.
// It mirrors CreateExpenseRequest's editable fields — PUT is a full replace of
// the editable representation. The claimant (user_id) and created_by are NOT
// editable and are never read from the body.
type UpdateExpenseRequest struct {
	CategoryID       string `json:"category_id"      binding:"required,uuid"`
	DatedOn          string `json:"dated_on"          binding:"required"` // YYYY-MM-DD
	Description      string `json:"description"       binding:"required,min=1"`
	CurrencyCode     string `json:"currency"          binding:"omitempty,len=3"` // defaults to GBP
	GrossValuePounds string `json:"gross_value"     binding:"required"`          // e.g. "42.50"

	// Optional fields — nil pointer means absent (omitted from the update body).
	ReceiptReference *string `json:"receipt_reference"`
	SupplierName     *string `json:"supplier_name"`
	SupplierVATNo    *string `json:"supplier_vat_number"`
	InvoiceNumber    *string `json:"invoice_number"`

	// VAT
	VATRateID *string `json:"vat_rate_id"`
	VATAmount *string `json:"vat_amount"` // pounds; used only for non-fixed-ratio rates (ignored for fixed-ratio)

	// Project rebilling
	ProjectID    *string `json:"project_id"`
	RebillType   *string `json:"rebill_type"`
	RebillFactor *string `json:"rebill_factor"`
}

// ChangeExpenseStatusRequest is the JSON body for POST /api/v1/expenses/:id/status.
// A single endpoint with an `action` discriminator drives the whole approval
// state machine (see expense_status.go) — one handler, the machine in one place.
//
// Validation is layered, like the contacts charge_vat field:
//   - binding here: `oneof` rejects an unknown action and `required_if` requires
//     a note when (and only when) rejecting (HTTP 400);
//   - the service re-checks both, independent of the HTTP layer (HTTP 422);
//   - the DB CHECK on expenses.status is the final backstop.
type ChangeExpenseStatusRequest struct {
	Action        string `json:"action"         binding:"required,oneof=submit approve reject reopen"`
	RejectionNote string `json:"rejection_note" binding:"required_if=Action reject"`
}

// ExpenseResponse is the JSON returned for a created or fetched expense.
// Amounts are returned as strings in pounds (e.g. "42.50") not raw pence,
// because JavaScript cannot safely represent large integers and clients
// should display formatted currency, not raw integers.
type ExpenseResponse struct {
	ID                string  `json:"id"`
	OrganisationID    string  `json:"organisation_id"`
	UserID            string  `json:"user_id"`
	CategoryID        string  `json:"category_id"`
	DatedOn           string  `json:"dated_on"`
	Description       string  `json:"description"`
	Currency          string  `json:"currency"`
	GrossValue        string  `json:"gross_value"`        // formatted pounds e.g. "42.50"
	NativeGrossValue  string  `json:"native_gross_value"` // in home currency
	VATValue          string  `json:"vat_value"`
	Status            string  `json:"status"`
	NeedsReview       bool    `json:"needs_review"` // TRUE while a Smart Upload capture awaits confirmation
	ReceiptReference  *string `json:"receipt_reference,omitempty"`
	SupplierName      *string `json:"supplier_name,omitempty"`
	SupplierVATNumber *string `json:"supplier_vat_number,omitempty"`
	InvoiceNumber     *string `json:"invoice_number,omitempty"`
	CreatedAt         string  `json:"created_at"`
	UpdatedAt         string  `json:"updated_at"`
}

// ExpenseDetailResponse is the richer JSON returned by GET /api/v1/expenses/:id.
// It comes from the v_expenses_full view (category + mileage joined) so a single
// call gives the detail screen everything: category name, VAT rate/status, FX /
// native values, EC status, project/rebill, and the approval timestamps. Money
// stays as pound strings; optional fields are omitted when null.
type ExpenseDetailResponse struct {
	ID          string `json:"id"`
	UserID      string `json:"user_id"` // claimant — raw FK for the edit form's "on behalf of" picker
	Status      string `json:"status"`
	DatedOn     string `json:"dated_on"`
	Description string `json:"description"`

	CategoryName        string `json:"category_name"`
	CategoryNominalCode string `json:"category_nominal_code"`
	CategoryID          string `json:"category_id"` // raw FK, for the edit form's picker

	Currency   string `json:"currency"`
	GrossValue string `json:"gross_value"`

	VATRateID *string `json:"vat_rate_id,omitempty"` // raw FK, for the edit form's picker
	VATRate   *string `json:"vat_rate,omitempty"`    // e.g. "20%"
	VATStatus string  `json:"vat_status"`
	VATValue  string  `json:"vat_value"`

	// Native / home-currency values — only differ from the above when the
	// expense was incurred in a foreign currency.
	NativeCurrency   string  `json:"native_currency"`
	NativeGrossValue string  `json:"native_gross_value"`
	NativeVATValue   string  `json:"native_vat_value"`
	ExchangeRate     *string `json:"exchange_rate,omitempty"`

	ECStatus string `json:"ec_status"`

	SupplierName      *string `json:"supplier_name,omitempty"`
	SupplierVATNumber *string `json:"supplier_vat_number,omitempty"`
	InvoiceNumber     *string `json:"invoice_number,omitempty"`
	ReceiptReference  *string `json:"receipt_reference,omitempty"`

	ProjectID    *string `json:"project_id,omitempty"`
	RebillType   *string `json:"rebill_type,omitempty"`
	RebillFactor *string `json:"rebill_factor,omitempty"`

	SubmittedAt      *string `json:"submitted_at,omitempty"`
	ApprovedAt       *string `json:"approved_at,omitempty"`
	ApprovedByUserID *string `json:"approved_by_user_id,omitempty"` // raw FK — who approved it
	PaidAt           *string `json:"paid_at,omitempty"`
	RejectionNote    *string `json:"rejection_note,omitempty"` // reason, set when REJECTED

	// Capture / OCR (Smart Upload). NeedsReview drives the review inbox;
	// OCRConfidence/OCRProcessedAt let the UI flag a low-confidence capture.
	// Attachments carries the file list — the frontend polls the primary
	// attachment's ocr_status here to know when OCR has filled the form.
	NeedsReview    bool                  `json:"needs_review"`
	OCRConfidence  *string               `json:"ocr_confidence,omitempty"`
	OCRProcessedAt *string               `json:"ocr_processed_at,omitempty"`
	Attachments    []*AttachmentResponse `json:"attachments"`

	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
}

// ExpenseCategoryResponse is the JSON returned for an expense category — the
// reference data the frontend uses to populate the category picker.
type ExpenseCategoryResponse struct {
	ID              string  `json:"id"`
	NominalCode     string  `json:"nominal_code"`
	Name            string  `json:"name"`
	CategoryGroup   *string `json:"category_group,omitempty"`
	Description     *string `json:"description,omitempty"`
	IsMileage       bool    `json:"is_mileage"`
	IsCapitalAsset  bool    `json:"is_capital_asset"`
	IsStockPurchase bool    `json:"is_stock_purchase"`
}

// VATRateResponse is the JSON returned for a VAT rate — reference data the
// frontend uses to populate the VAT rate picker and to compute VAT amounts.
//
// The rate is exposed two ways on purpose:
//   - rate_bps: the canonical integer (basis points, 2000 = 20.00%) the client
//     should use for EXACT computation (gross × rate_bps / 10000).
//   - rate: a ready-to-display string ("20%") so the dropdown doesn't have to
//     format it.
//
// is_fixed_ratio tells the client whether the VAT amount is locked to
// gross × rate (true) or whether the user may enter a custom amount (false).
type VATRateResponse struct {
	ID           string `json:"id"`
	Name         string `json:"name"`           // e.g. "Standard Rate"
	RateBps      int32  `json:"rate_bps"`       // basis points: 2000 = 20.00%
	Rate         string `json:"rate"`           // display form, e.g. "20%"
	IsFixedRatio bool   `json:"is_fixed_ratio"` // true = amount locked to gross × rate
}

// UpdateProfileRequest is the JSON body accepted by PUT /api/v1/profile (the
// "My Details" screen). It carries only the editable profile fields. The login
// email is deliberately absent (changing it is a separate, security-sensitive
// flow), as are phone/avatar_url — the service preserves those. Both names are
// required (they are NOT NULL on the users table and starred on the form); `max`
// matches the VARCHAR(100) columns. The user is taken from the token, so there
// is no id here. The GET/PUT responses reuse the login userResponse shape.
type UpdateProfileRequest struct {
	FirstName string `json:"first_name" binding:"required,max=100"`
	LastName  string `json:"last_name" binding:"required,max=100"`
}

// =============================================================================
// HANDLERS
// =============================================================================

// handleCreateExpense handles POST /api/v1/expenses
//
// Flow:
//  1. Bind JSON body into CreateExpenseRequest (validates required fields)
//  2. Extract organisation_id from context (set by auth middleware — stubbed here)
//  3. Call expenseService.CreateExpense
//  4. Return 201 Created with the new expense as JSON
func (s *Server) handleCreateExpense(c *gin.Context) {
	// Step 1: bind + validate the request body. bindJSON deserialises the JSON,
	// runs the `binding:` tag validations, and on failure writes the standard 400
	// error envelope and returns false.
	var req CreateExpenseRequest
	if !bindJSON(c, &req) {
		return
	}

	// Step 2: Identify the caller from the authenticated token (set by
	// authMiddleware). The claimant and organisation come from here — never from
	// the request body — so a user can only create expenses for themselves.
	userID := getAuthUserID(c)
	orgID := getAuthOrgID(c)

	// Step 3: Call the service layer.
	// The service handles authorisation, business logic, unit conversion, and
	// database writes.
	expense, err := s.expenseService.CreateExpense(c.Request.Context(), userID, orgID, req)
	if err != nil {
		respondError(c, err)
		return
	}

	// Step 4: Return 201 Created.
	// gin.H is just map[string]any — a shorthand for building JSON objects.
	c.JSON(http.StatusCreated, gin.H{"expense": expense})
}

// handleGetExpense handles GET /api/v1/expenses/:id
func (s *Server) handleGetExpense(c *gin.Context) {
	// c.Param("id") extracts the :id segment from the URL path.
	// e.g. GET /api/v1/expenses/abc-123 → id = "abc-123"
	id := c.Param("id")

	userID := getAuthUserID(c)
	orgID := getAuthOrgID(c)

	expense, err := s.expenseService.GetExpenseDetail(c.Request.Context(), userID, orgID, id)

	if err != nil {
		respondError(c, err)
		return
	}

	c.JSON(http.StatusOK, gin.H{"expense": expense})
}

// handleUpdateExpense handles PUT /api/v1/expenses/:id
//
// Full update of an expense's editable fields. The service enforces that the
// caller owns the expense (or is an owner/admin of the org) and that the expense
// is still editable (DRAFT or REJECTED).
func (s *Server) handleUpdateExpense(c *gin.Context) {
	id := c.Param("id")

	var req UpdateExpenseRequest
	if !bindJSON(c, &req) {
		return
	}

	userID := getAuthUserID(c)
	orgID := getAuthOrgID(c)

	expense, err := s.expenseService.UpdateExpense(c.Request.Context(), userID, orgID, id, req)
	if err != nil {
		respondError(c, err)
		return
	}

	c.JSON(http.StatusOK, gin.H{"expense": expense})
}

// handleChangeExpenseStatus handles POST /api/v1/expenses/:id/status
//
// Drives one approval-workflow transition (submit/approve/reject/reopen). The
// service enforces the state machine (legal from→to, 409 on a bad move) and
// authorisation (claimant-or-admin for submit/reopen, admin-only for
// approve/reject). On success it returns the updated expense, like update.
func (s *Server) handleChangeExpenseStatus(c *gin.Context) {
	id := c.Param("id")

	var req ChangeExpenseStatusRequest
	if !bindJSON(c, &req) {
		return
	}

	userID := getAuthUserID(c)
	orgID := getAuthOrgID(c)

	expense, err := s.expenseService.ChangeExpenseStatus(c.Request.Context(), userID, orgID, id, req.Action, req.RejectionNote)
	if err != nil {
		respondError(c, err)
		return
	}

	c.JSON(http.StatusOK, gin.H{"expense": expense})
}

// handleDeleteExpense handles DELETE /api/v1/expenses/:id
//
// Soft-deletes an expense (e.g. an abandoned Smart Upload draft). The service
// enforces that the caller owns the expense (or is an owner/admin of the org) and
// that it is still editable (DRAFT or REJECTED). Returns 204 No Content.
func (s *Server) handleDeleteExpense(c *gin.Context) {
	id := c.Param("id")
	userID := getAuthUserID(c)
	orgID := getAuthOrgID(c)

	if err := s.expenseService.DeleteExpense(c.Request.Context(), userID, orgID, id); err != nil {
		respondError(c, err)
		return
	}

	c.Status(http.StatusNoContent)
}

// handleListExpenses handles GET /api/v1/expenses
//
// Returns the expenses the caller is allowed to see: owners/admins get every
// expense in their organisation; everyone else gets only their own.
func (s *Server) handleListExpenses(c *gin.Context) {
	userID := getAuthUserID(c)
	orgID := getAuthOrgID(c)

	list, err := s.expenseService.ListExpenses(c.Request.Context(), userID, orgID)
	if err != nil {
		respondError(c, err)
		return
	}

	c.JSON(http.StatusOK, gin.H{"expenses": list})
}

// handleExportExpenses handles POST /api/v1/expenses/export
//
// Streams expenses as a CSV download. The (optional) JSON body carries the ids to
// export so the SPA can export exactly the rows it filtered for display:
//
//	{"ids": ["<uuid>", ...]}  → export exactly those (empty list → header only)
//	no body / no "ids"        → export everything the caller may see
//
// Either way the backend enforces the visibility rule (owners/admins: the whole
// org; everyone else: only their own) and the column set matches the import
// template at web/public/expense_import_template.csv, so an export round-trips.
func (s *Server) handleExportExpenses(c *gin.Context) {
	userID := getAuthUserID(c)
	orgID := getAuthOrgID(c)

	// Optional body. A bare POST (no body) means "everything I may see"; a body
	// with "ids" (even an empty array) means "exactly these". *[]string lets us
	// tell an absent "ids" (nil → all) from an empty one ([] → none).
	var req struct {
		Ids *[]string `json:"ids"`
	}
	if c.Request.ContentLength != 0 {
		if !bindJSON(c, &req) {
			return
		}
	}

	var ids []uuid.UUID
	if req.Ids != nil {
		ids = make([]uuid.UUID, 0, len(*req.Ids))
		for _, raw := range *req.Ids {
			id, err := uuid.Parse(raw)
			if err != nil {
				c.JSON(http.StatusUnprocessableEntity, gin.H{"error": gin.H{
					"code":    "validation_error",
					"message": fmt.Sprintf("invalid expense id %q", raw),
				}})
				return
			}
			ids = append(ids, id)
		}
	}

	rows, err := s.expenseService.ExportExpenses(c.Request.Context(), userID, orgID, ids)
	if err != nil {
		respondError(c, err)
		return
	}

	// Past this point we commit to a 200: set the download headers, then stream
	// the CSV straight to the response writer. Write errors here only happen if
	// the client disconnects mid-stream — there's nothing useful to return.
	filename := fmt.Sprintf("expenses-%s.csv", time.Now().Format("2006-01-02"))
	c.Header("Content-Type", "text/csv; charset=utf-8")
	c.Header("Content-Disposition", fmt.Sprintf("attachment; filename=%q", filename))

	w := csv.NewWriter(c.Writer)
	_ = w.Write(expenseExportHeader) // header row first
	for _, r := range rows {
		_ = w.Write(r.record())
	}
	w.Flush()
}

// handleListInbox handles GET /api/v1/expenses/inbox
//
// Returns the Smart Upload captures awaiting review (needs_review). Owners/admins
// see the whole organisation's inbox; everyone else sees only their own captures.
func (s *Server) handleListInbox(c *gin.Context) {
	list, err := s.expenseService.ListInbox(c.Request.Context(), getAuthUserID(c), getAuthOrgID(c))
	if err != nil {
		respondError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"expenses": list})
}

// handleListExpenseCategories handles GET /api/v1/expense-categories
//
// Returns the active expense categories for the caller's organisation (taken
// from the authenticated token), used to populate the category picker.
func (s *Server) handleListExpenseCategories(c *gin.Context) {
	userID := getAuthUserID(c)
	orgID := getAuthOrgID(c)

	list, err := s.expenseService.ListExpenseCategories(c.Request.Context(), userID, orgID)
	if err != nil {
		respondError(c, err)
		return
	}

	c.JSON(http.StatusOK, gin.H{"expense_categories": list})
}

// handleListVATRates handles GET /api/v1/vat-rates
//
// Returns the VAT rates valid today for the caller's organisation's country
// (resolved from the authenticated token, never a request param), used to
// populate the VAT rate picker.
func (s *Server) handleListVATRates(c *gin.Context) {
	userID := getAuthUserID(c)
	orgID := getAuthOrgID(c)

	list, err := s.expenseService.ListVATRates(c.Request.Context(), userID, orgID)
	if err != nil {
		respondError(c, err)
		return
	}

	c.JSON(http.StatusOK, gin.H{"vat_rates": list})
}

// =============================================================================
// CONTACT HANDLERS
//
// Thin HTTP boundary, identical in shape to the expense handlers: bind the body
// (where applicable), take the caller + org from the token via getAuthUserID /
// getAuthOrgID, call the service, and translate any error through AsAppError so
// the right status + safe JSON go back.
// =============================================================================

// =============================================================================
// PROJECT REQUEST / RESPONSE TYPES
// =============================================================================

// =============================================================================
// MEMBER HANDLERS
// =============================================================================

// handleGetProfile handles GET /api/v1/profile — the caller's own "My Details".
// The user is taken from the token, so a caller can only ever read themselves.
func (s *Server) handleGetProfile(c *gin.Context) {
	profile, err := s.userService.GetProfile(c.Request.Context(), getAuthUserID(c))
	if err != nil {
		respondError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"user": profile})
}

// handleUpdateProfile handles PUT /api/v1/profile — update the caller's first/
// last name. The user is taken from the token, so it always targets themselves.
func (s *Server) handleUpdateProfile(c *gin.Context) {
	var req UpdateProfileRequest
	if !bindJSON(c, &req) {
		return
	}

	profile, err := s.userService.UpdateProfile(c.Request.Context(), getAuthUserID(c), req)
	if err != nil {
		respondError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"user": profile})
}
