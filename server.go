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
	"net/http"

	"github.com/gin-gonic/gin"
)

// Server holds the Gin engine and all the services it needs to handle requests.
// Adding a new service module (invoices, contacts, etc.) means adding a field
// here and passing it into NewServer.
type Server struct {
	router         *gin.Engine
	expenseService *ExpenseService
}

// NewServer constructs the Server, registers all routes, and returns it.
// main.go calls this once at startup.
func NewServer(expenseService *ExpenseService) *Server {
	s := &Server{
		expenseService: expenseService,
	}

	// gin.Default() creates a Gin engine with two built-in middleware:
	//   - Logger: prints each request (method, path, status, latency) to stdout
	//   - Recovery: catches panics and returns 500 instead of crashing the server
	// For production you'd replace these with structured logging middleware,
	// but Default() is the right starting point.
	s.router = gin.Default()

	s.registerRoutes()

	return s
}

// Run starts the HTTP server on the given address (e.g. ":8080").
// It blocks until the server stops.
func (s *Server) Run(addr string) error {
	return s.router.Run(addr)
}

// registerRoutes declares every URL pattern and which handler method responds.
// Keeping all routes in one place makes it easy to see the full API surface.
func (s *Server) registerRoutes() {
	// Route groups let you share a URL prefix and (later) middleware.
	// All expense routes live under /api/v1/expenses.
	// Versioning (/v1/) in the URL means you can introduce /v2/ later
	// without breaking existing clients.
	v1 := s.router.Group("/api/v1")
	{
		expenses := v1.Group("/expenses")
		{
			// POST   /api/v1/expenses       → create a new expense
			// GET    /api/v1/expenses/:id   → fetch one expense by UUID
			expenses.POST("", s.handleCreateExpense)
			expenses.GET("/:id", s.handleGetExpense)
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
	UserID           string `json:"user_id"          binding:"required,uuid"`
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

	// VAT
	VATRateID *string `json:"vat_rate_id"` // UUID of the applicable VAT rate

	// Project rebilling — all three must be provided together if rebilling
	ProjectID    *string `json:"project_id"`
	RebillType   *string `json:"rebill_type"`   // "cost" | "markup" | "price"
	RebillFactor *string `json:"rebill_factor"` // decimal string e.g. "1.15"
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
	ReceiptReference  *string `json:"receipt_reference,omitempty"`
	SupplierName      *string `json:"supplier_name,omitempty"`
	SupplierVATNumber *string `json:"supplier_vat_number,omitempty"`
	InvoiceNumber     *string `json:"invoice_number,omitempty"`
	CreatedAt         string  `json:"created_at"`
	UpdatedAt         string  `json:"updated_at"`
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
	// Step 1: Parse and validate the request body.
	// ShouldBindJSON reads c.Request.Body, deserialises JSON into the struct,
	// and runs the `binding:` tag validations. If anything fails it returns
	// a non-nil error describing what went wrong.
	var req CreateExpenseRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		// 400 Bad Request — the client sent invalid data.
		// err.Error() gives a human-readable validation message.
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	// Step 2: Get the organisation ID.
	// In a real system this comes from a JWT token validated by auth middleware.
	// The middleware decodes the token and sets "organisation_id" in the Gin
	// context (c.Set("organisation_id", id)). Here we stub it with a fixed
	// value so the code compiles and runs without auth built yet.
	// TODO: replace with real auth middleware
	orgID := "00000000-0000-0000-0000-000000000001"

	// Step 3: Call the service layer.
	// The service handles business logic, unit conversion, and database writes.
	expense, err := s.expenseService.CreateExpense(c.Request.Context(), orgID, req)
	/*if err != nil {
		// 500 Internal Server Error — something went wrong on our side.
		// In production you'd inspect the error type and return 422/409/etc.
		// for known business rule violations. We'll improve this later.
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}*/
	if err != nil {
		// AsAppError converts any error into *AppError.
		// If the service returned an *AppError (e.g. ErrValidation, ErrNotFound),
		// it is returned as-is. If it returned a plain error (unexpected), it is
		// wrapped in ErrInternal so the handler always has a typed error to work with.
		appErr := AsAppError(err)
		if appErr.Code == ErrCodeInternal {
			// TODO: replace with structured logger (e.g. slog or zap)
			_ = appErr.Error() // placeholder for: logger.Error(appErr.Error())
		}
		// ClientResponse() returns only {code, message} — never the internal cause.
		c.JSON(appErr.HTTPStatus(), gin.H{"error": appErr.ClientResponse()})
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

	// Stub org ID — same note as above
	orgID := "00000000-0000-0000-0000-000000000001"

	expense, err := s.expenseService.GetExpense(c.Request.Context(), id, orgID)

	/*if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	} */

	if err != nil {
		appErr := AsAppError(err)
		if appErr.Code == ErrCodeInternal {
			_ = appErr.Error() // TODO: structured logger
		}
		c.JSON(appErr.HTTPStatus(), gin.H{"error": appErr.ClientResponse()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"expense": expense})
}
