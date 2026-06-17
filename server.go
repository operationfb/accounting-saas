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
	"time"

	"github.com/gin-contrib/cors"
	"github.com/gin-gonic/gin"

	"github.com/operationfb/accounting-saas/token"
)

// Server holds the Gin engine and all the services it needs to handle requests.
// Adding a new service module (invoices, contacts, etc.) means adding a field
// here and passing it into NewServer.
type Server struct {
	router              *gin.Engine
	expenseService      *ExpenseService
	attachmentService   *AttachmentService
	contactService      *ContactService
	projectService      *ProjectService
	memberService       *MemberService
	organisationService *OrganisationService
	userService         *UserService
	emailInboxService   *EmailInboxService
	authHandler         *AuthHandler
	tokenMaker          token.Maker

	// mailgunSigningKey authenticates the inbound-email webhook (HMAC). Empty
	// when the channel isn't configured, in which case the webhook isn't mounted.
	mailgunSigningKey string
}

// NewServer constructs the Server, registers all routes, and returns it.
// main.go calls this once at startup.
func NewServer(expenseService *ExpenseService, attachmentService *AttachmentService, contactService *ContactService, projectService *ProjectService, memberService *MemberService, organisationService *OrganisationService, userService *UserService, emailInboxService *EmailInboxService, authHandler *AuthHandler, tokenMaker token.Maker, mailgunSigningKey string, corsOrigins []string) *Server {
	s := &Server{
		expenseService:      expenseService,
		attachmentService:   attachmentService,
		contactService:      contactService,
		projectService:      projectService,
		memberService:       memberService,
		organisationService: organisationService,
		userService:         userService,
		emailInboxService:   emailInboxService,
		authHandler:         authHandler,
		tokenMaker:          tokenMaker,
		mailgunSigningKey:   mailgunSigningKey,
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

// registerRoutes declares every URL pattern and which handler method responds.
// Keeping all routes in one place makes it easy to see the full API surface.
func (s *Server) registerRoutes() {
	// Route groups let you share a URL prefix and (later) middleware.
	// All expense routes live under /api/v1/expenses.
	// Versioning (/v1/) in the URL means you can introduce /v2/ later
	// without breaking existing clients.
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

		// Contact routes require a valid login. Like expenses, the organisation is
		// taken from the bearer token, so every query is automatically org-scoped.
		contacts := v1.Group("/contacts")
		contacts.Use(authMiddleware(s.tokenMaker))
		{
			// GET    /api/v1/contacts      → list the org's contacts
			// POST   /api/v1/contacts      → create a contact
			// GET    /api/v1/contacts/:id  → fetch one contact by UUID
			// PUT    /api/v1/contacts/:id  → full update (creator or owner/admin)
			// DELETE /api/v1/contacts/:id  → soft-delete (creator or owner/admin)
			contacts.GET("", s.handleListContacts)
			contacts.POST("", s.handleCreateContact)
			contacts.GET("/:id", s.handleGetContact)
			contacts.PUT("/:id", s.handleUpdateContact)
			contacts.DELETE("/:id", s.handleDeleteContact)
		}

		// Project routes require a valid login. Organisation is taken from the
		// bearer token, so every query is automatically org-scoped.
		projectRoutes := v1.Group("/projects")
		projectRoutes.Use(authMiddleware(s.tokenMaker))
		{
			// GET    /api/v1/projects      → list the org's projects
			// POST   /api/v1/projects      → create a project
			// GET    /api/v1/projects/:id  → fetch one project by UUID
			// PUT    /api/v1/projects/:id  → full update
			// DELETE /api/v1/projects/:id  → hard delete
			projectRoutes.GET("", s.handleListProjects)
			projectRoutes.POST("", s.handleCreateProject)
			projectRoutes.GET("/:id", s.handleGetProject)
			projectRoutes.PUT("/:id", s.handleUpdateProject)
			projectRoutes.DELETE("/:id", s.handleDeleteProject)
		}

		// Member routes require a valid login. The organisation is taken from the
		// bearer token, so the listing is automatically scoped to the caller's org
		// — and the handler/service additionally restricts it to owners/admins.
		members := v1.Group("/members")
		members.Use(authMiddleware(s.tokenMaker))
		{
			// GET /api/v1/members → list the org's members (owner/admin only)
			members.GET("", s.handleListMembers)
		}

		// Organisation routes require a valid login. There is always exactly one
		// organisation in scope (the caller's, from the bearer token), so these act
		// on a singleton resource — no id in the path.
		organisation := v1.Group("/organisation")
		organisation.Use(authMiddleware(s.tokenMaker))
		{
			// GET /api/v1/organisation → the caller's company details (any active member)
			// PUT /api/v1/organisation → update company details (owner/admin only)
			organisation.GET("", s.handleGetOrganisation)
			organisation.PUT("", s.handleUpdateOrganisation)
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

// CreateContactRequest is the JSON body accepted by POST /api/v1/contacts.
// Almost every field is optional (pointer = absent → NULL), matching the form's
// permissive shape: a contact may be a person, a company, or both. The owning
// organisation and creator are taken from the token, never the body.
//
// Notes:
//   - charge_vat is validated by `oneof`; omitted → service default SAME_COUNTRY.
//   - country_code omitted → service default GB (and it is upper-cased).
//   - display_contact_name is a *bool so an omitted value can default to TRUE
//     (the form's checked-by-default box) rather than Go's zero value false.
//   - default_payment_terms_days is a *int32 so 0 ("Due on Receipt") is distinct
//     from absent (no contact-level terms → NULL).
type CreateContactRequest struct {
	FirstName        *string `json:"first_name"`
	LastName         *string `json:"last_name"`
	OrganisationName *string `json:"organisation_name"`
	Email            *string `json:"email"         binding:"omitempty,email"`
	BillingEmail     *string `json:"billing_email" binding:"omitempty,email"`
	Telephone        *string `json:"telephone"`
	Mobile           *string `json:"mobile"`

	AddressLine1 *string `json:"address_line_1"`
	AddressLine2 *string `json:"address_line_2"`
	AddressLine3 *string `json:"address_line_3"`
	Town         *string `json:"town"`
	Region       *string `json:"region"`
	Postcode     *string `json:"postcode"`
	CountryCode  string  `json:"country_code" binding:"omitempty,len=2"` // ISO 3166-1 alpha-2; defaults to GB

	DefaultPaymentTermsDays         *int32  `json:"default_payment_terms_days" binding:"omitempty,min=0"`
	UsesContactLevelEmailSettings   bool    `json:"uses_contact_level_email_settings"`
	UsesContactLevelInvoiceSequence bool    `json:"uses_contact_level_invoice_sequence"`
	DisplayContactName              *bool   `json:"display_contact_name"` // nil → default TRUE
	ChargeVAT                       string  `json:"charge_vat" binding:"omitempty,oneof=ALWAYS NEVER SAME_COUNTRY"`
	VATRegistrationNumber           *string `json:"vat_registration_number"`
	InvoiceLanguage                 string  `json:"invoice_language"` // defaults to "en"

	BankSortCode      *string `json:"bank_sort_code"`
	BankAccountNumber *string `json:"bank_account_number"`
	BankRecipientName *string `json:"bank_recipient_name"`
}

// UpdateContactRequest is the JSON body accepted by PUT /api/v1/contacts/:id.
// It mirrors CreateContactRequest's editable fields — PUT is a full replace of
// the editable representation. organisation_id and created_by are never read
// from the body.
type UpdateContactRequest struct {
	FirstName        *string `json:"first_name"`
	LastName         *string `json:"last_name"`
	OrganisationName *string `json:"organisation_name"`
	Email            *string `json:"email"         binding:"omitempty,email"`
	BillingEmail     *string `json:"billing_email" binding:"omitempty,email"`
	Telephone        *string `json:"telephone"`
	Mobile           *string `json:"mobile"`

	AddressLine1 *string `json:"address_line_1"`
	AddressLine2 *string `json:"address_line_2"`
	AddressLine3 *string `json:"address_line_3"`
	Town         *string `json:"town"`
	Region       *string `json:"region"`
	Postcode     *string `json:"postcode"`
	CountryCode  string  `json:"country_code" binding:"omitempty,len=2"`

	DefaultPaymentTermsDays         *int32  `json:"default_payment_terms_days" binding:"omitempty,min=0"`
	UsesContactLevelEmailSettings   bool    `json:"uses_contact_level_email_settings"`
	UsesContactLevelInvoiceSequence bool    `json:"uses_contact_level_invoice_sequence"`
	DisplayContactName              *bool   `json:"display_contact_name"`
	ChargeVAT                       string  `json:"charge_vat" binding:"omitempty,oneof=ALWAYS NEVER SAME_COUNTRY"`
	VATRegistrationNumber           *string `json:"vat_registration_number"`
	InvoiceLanguage                 string  `json:"invoice_language"`

	BankSortCode      *string `json:"bank_sort_code"`
	BankAccountNumber *string `json:"bank_account_number"`
	BankRecipientName *string `json:"bank_recipient_name"`
}

// ContactResponse is the JSON returned for a created/fetched/updated contact.
// Nullable columns are returned as omitempty pointers; the never-null
// invoicing-option fields are returned as plain values.
type ContactResponse struct {
	ID              string `json:"id"`
	OrganisationID  string `json:"organisation_id"`
	CreatedByUserID string `json:"created_by_user_id"`

	FirstName        *string `json:"first_name,omitempty"`
	LastName         *string `json:"last_name,omitempty"`
	OrganisationName *string `json:"organisation_name,omitempty"`
	Email            *string `json:"email,omitempty"`
	BillingEmail     *string `json:"billing_email,omitempty"`
	Telephone        *string `json:"telephone,omitempty"`
	Mobile           *string `json:"mobile,omitempty"`

	AddressLine1 *string `json:"address_line_1,omitempty"`
	AddressLine2 *string `json:"address_line_2,omitempty"`
	AddressLine3 *string `json:"address_line_3,omitempty"`
	Town         *string `json:"town,omitempty"`
	Region       *string `json:"region,omitempty"`
	Postcode     *string `json:"postcode,omitempty"`
	CountryCode  string  `json:"country_code"`

	DefaultPaymentTermsDays         *int32  `json:"default_payment_terms_days,omitempty"`
	UsesContactLevelEmailSettings   bool    `json:"uses_contact_level_email_settings"`
	UsesContactLevelInvoiceSequence bool    `json:"uses_contact_level_invoice_sequence"`
	DisplayContactName              bool    `json:"display_contact_name"`
	ChargeVAT                       string  `json:"charge_vat"`
	VATRegistrationNumber           *string `json:"vat_registration_number,omitempty"`
	InvoiceLanguage                 string  `json:"invoice_language"`

	BankSortCode      *string `json:"bank_sort_code,omitempty"`
	BankAccountNumber *string `json:"bank_account_number,omitempty"`
	BankRecipientName *string `json:"bank_recipient_name,omitempty"`

	IsActive  bool   `json:"is_active"`
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
}

// MemberResponse is the JSON returned for one organisation member (a membership
// joined to its user). It deliberately exposes only what a "Team / Manage users"
// screen needs — no password hash or other secrets. UUIDs are strings and
// timestamps are RFC3339; avatar_url and last_login_at are nullable (omitted when
// absent). role and status are the membership enum/status values, so the UI can
// badge each member.
type MemberResponse struct {
	MembershipID string  `json:"membership_id"`
	UserID       string  `json:"user_id"`
	Email        string  `json:"email"`
	FirstName    string  `json:"first_name"`
	LastName     string  `json:"last_name"`
	Role         string  `json:"role"`   // owner | admin | member | accountant | read_only
	Status       string  `json:"status"` // active | invited | suspended | deactivated
	AvatarURL    *string `json:"avatar_url,omitempty"`
	MemberSince  string  `json:"member_since"` // RFC3339 (membership created_at)
	LastLoginAt  *string `json:"last_login_at,omitempty"`
}

// UpdateOrganisationRequest is the JSON body accepted by PUT /api/v1/organisation
// (the Company Details screen). It carries the editable company-detail fields.
// Fields the form does not own — slug, native_currency, timezone and (until VAT
// is added) vrn — are deliberately absent: the service preserves them. name is
// the organisation's primary name and is required; everything else is optional
// (a nil pointer → NULL). The owning organisation is taken from the token.
type UpdateOrganisationRequest struct {
	Name        string  `json:"name" binding:"required"`
	LegalName   *string `json:"legal_name"`
	CompanyType *string `json:"company_type" binding:"omitempty,oneof=limited sole_trader partnership landlord corporation"`

	CompaniesHouseNumber    *string `json:"companies_house_number"`
	Utr                     *string `json:"utr"` // "Corporation Tax Reference" on the form
	PayeReference           *string `json:"paye_reference"`
	AccountsOfficeReference *string `json:"accounts_office_reference"`

	AddressLine1 *string `json:"address_line_1"`
	AddressLine2 *string `json:"address_line_2"`
	AddressLine3 *string `json:"address_line_3"`
	Town         *string `json:"town"`
	Region       *string `json:"region"`
	Postcode     *string `json:"postcode"`
	CountryCode  string  `json:"country_code" binding:"omitempty,len=2"` // ISO 3166-1 alpha-2; defaults to GB

	BusinessPhone *string `json:"business_phone"`
	ContactEmail  *string `json:"contact_email" binding:"omitempty,email"`
	ContactPhone  *string `json:"contact_phone"`
	Website       *string `json:"website"`

	BusinessCategory    *string `json:"business_category"`
	BusinessDescription *string `json:"business_description"`
}

// OrganisationDetailsResponse is the JSON returned by GET/PUT /api/v1/organisation.
// Nullable columns are omitempty pointers; the never-null fields are plain values.
// The legacy free-text registered_address is not exposed (superseded by the
// structured address). native_currency / timezone / plan / is_active are returned
// read-only for the frontend even though this screen doesn't edit them.
type OrganisationDetailsResponse struct {
	ID          string  `json:"id"`
	Name        string  `json:"name"`
	Slug        *string `json:"slug,omitempty"`
	LegalName   *string `json:"legal_name,omitempty"`
	CompanyType *string `json:"company_type,omitempty"`

	CompaniesHouseNumber    *string `json:"companies_house_number,omitempty"`
	Utr                     *string `json:"utr,omitempty"`
	Vrn                     *string `json:"vrn,omitempty"`
	PayeReference           *string `json:"paye_reference,omitempty"`
	AccountsOfficeReference *string `json:"accounts_office_reference,omitempty"`

	AddressLine1 *string `json:"address_line_1,omitempty"`
	AddressLine2 *string `json:"address_line_2,omitempty"`
	AddressLine3 *string `json:"address_line_3,omitempty"`
	Town         *string `json:"town,omitempty"`
	Region       *string `json:"region,omitempty"`
	Postcode     *string `json:"postcode,omitempty"`
	CountryCode  string  `json:"country_code"`

	BusinessPhone *string `json:"business_phone,omitempty"`
	ContactEmail  *string `json:"contact_email,omitempty"`
	ContactPhone  *string `json:"contact_phone,omitempty"`
	Website       *string `json:"website,omitempty"`

	BusinessCategory    *string `json:"business_category,omitempty"`
	BusinessDescription *string `json:"business_description,omitempty"`

	NativeCurrency string `json:"native_currency"`
	Timezone       string `json:"timezone"`
	Plan           string `json:"plan"`
	IsActive       bool   `json:"is_active"`
	CreatedAt      string `json:"created_at"`
	UpdatedAt      string `json:"updated_at"`
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

	// Step 2: Identify the caller from the authenticated token (set by
	// authMiddleware). The claimant and organisation come from here — never from
	// the request body — so a user can only create expenses for themselves.
	userID := getAuthUserID(c)
	orgID := getAuthOrgID(c)

	// Step 3: Call the service layer.
	// The service handles authorisation, business logic, unit conversion, and
	// database writes.
	expense, err := s.expenseService.CreateExpense(c.Request.Context(), userID, orgID, req)
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

	userID := getAuthUserID(c)
	orgID := getAuthOrgID(c)

	expense, err := s.expenseService.GetExpenseDetail(c.Request.Context(), userID, orgID, id)

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

// handleUpdateExpense handles PUT /api/v1/expenses/:id
//
// Full update of an expense's editable fields. The service enforces that the
// caller owns the expense (or is an owner/admin of the org) and that the expense
// is still editable (DRAFT or REJECTED).
func (s *Server) handleUpdateExpense(c *gin.Context) {
	id := c.Param("id")

	var req UpdateExpenseRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	userID := getAuthUserID(c)
	orgID := getAuthOrgID(c)

	expense, err := s.expenseService.UpdateExpense(c.Request.Context(), userID, orgID, id, req)
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

// handleChangeExpenseStatus handles POST /api/v1/expenses/:id/status
//
// Drives one approval-workflow transition (submit/approve/reject/reopen). The
// service enforces the state machine (legal from→to, 409 on a bad move) and
// authorisation (claimant-or-admin for submit/reopen, admin-only for
// approve/reject). On success it returns the updated expense, like update.
func (s *Server) handleChangeExpenseStatus(c *gin.Context) {
	id := c.Param("id")

	var req ChangeExpenseStatusRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	userID := getAuthUserID(c)
	orgID := getAuthOrgID(c)

	expense, err := s.expenseService.ChangeExpenseStatus(c.Request.Context(), userID, orgID, id, req.Action, req.RejectionNote)
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
		appErr := AsAppError(err)
		if appErr.Code == ErrCodeInternal {
			_ = appErr.Error() // TODO: structured logger
		}
		c.JSON(appErr.HTTPStatus(), gin.H{"error": appErr.ClientResponse()})
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
		appErr := AsAppError(err)
		if appErr.Code == ErrCodeInternal {
			_ = appErr.Error() // TODO: structured logger
		}
		c.JSON(appErr.HTTPStatus(), gin.H{"error": appErr.ClientResponse()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"expenses": list})
}

// handleListInbox handles GET /api/v1/expenses/inbox
//
// Returns the Smart Upload captures awaiting review (needs_review). Owners/admins
// see the whole organisation's inbox; everyone else sees only their own captures.
func (s *Server) handleListInbox(c *gin.Context) {
	list, err := s.expenseService.ListInbox(c.Request.Context(), getAuthUserID(c), getAuthOrgID(c))
	if err != nil {
		appErr := AsAppError(err)
		if appErr.Code == ErrCodeInternal {
			_ = appErr.Error() // TODO: structured logger
		}
		c.JSON(appErr.HTTPStatus(), gin.H{"error": appErr.ClientResponse()})
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
		appErr := AsAppError(err)
		if appErr.Code == ErrCodeInternal {
			_ = appErr.Error() // TODO: structured logger
		}
		c.JSON(appErr.HTTPStatus(), gin.H{"error": appErr.ClientResponse()})
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
		appErr := AsAppError(err)
		if appErr.Code == ErrCodeInternal {
			_ = appErr.Error() // TODO: structured logger
		}
		c.JSON(appErr.HTTPStatus(), gin.H{"error": appErr.ClientResponse()})
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

// handleListContacts handles GET /api/v1/contacts — every contact in the
// caller's organisation.
func (s *Server) handleListContacts(c *gin.Context) {
	list, err := s.contactService.ListContacts(c.Request.Context(), getAuthUserID(c), getAuthOrgID(c))
	if err != nil {
		appErr := AsAppError(err)
		if appErr.Code == ErrCodeInternal {
			_ = appErr.Error() // TODO: structured logger
		}
		c.JSON(appErr.HTTPStatus(), gin.H{"error": appErr.ClientResponse()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"contacts": list})
}

// handleCreateContact handles POST /api/v1/contacts — create one contact for the
// caller's organisation. Returns 201 Created.
func (s *Server) handleCreateContact(c *gin.Context) {
	var req CreateContactRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	contact, err := s.contactService.CreateContact(c.Request.Context(), getAuthUserID(c), getAuthOrgID(c), req)
	if err != nil {
		appErr := AsAppError(err)
		if appErr.Code == ErrCodeInternal {
			_ = appErr.Error() // TODO: structured logger
		}
		c.JSON(appErr.HTTPStatus(), gin.H{"error": appErr.ClientResponse()})
		return
	}

	c.JSON(http.StatusCreated, gin.H{"contact": contact})
}

// handleGetContact handles GET /api/v1/contacts/:id.
func (s *Server) handleGetContact(c *gin.Context) {
	contact, err := s.contactService.GetContact(c.Request.Context(), getAuthUserID(c), getAuthOrgID(c), c.Param("id"))
	if err != nil {
		appErr := AsAppError(err)
		if appErr.Code == ErrCodeInternal {
			_ = appErr.Error() // TODO: structured logger
		}
		c.JSON(appErr.HTTPStatus(), gin.H{"error": appErr.ClientResponse()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"contact": contact})
}

// handleUpdateContact handles PUT /api/v1/contacts/:id — full update, allowed to
// the contact's creator or an owner/admin of the organisation.
func (s *Server) handleUpdateContact(c *gin.Context) {
	var req UpdateContactRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	contact, err := s.contactService.UpdateContact(c.Request.Context(), getAuthUserID(c), getAuthOrgID(c), c.Param("id"), req)
	if err != nil {
		appErr := AsAppError(err)
		if appErr.Code == ErrCodeInternal {
			_ = appErr.Error() // TODO: structured logger
		}
		c.JSON(appErr.HTTPStatus(), gin.H{"error": appErr.ClientResponse()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"contact": contact})
}

// handleDeleteContact handles DELETE /api/v1/contacts/:id — soft-delete, allowed
// to the contact's creator or an owner/admin. Returns 204 No Content.
func (s *Server) handleDeleteContact(c *gin.Context) {
	if err := s.contactService.DeleteContact(c.Request.Context(), getAuthUserID(c), getAuthOrgID(c), c.Param("id")); err != nil {
		appErr := AsAppError(err)
		if appErr.Code == ErrCodeInternal {
			_ = appErr.Error() // TODO: structured logger
		}
		c.JSON(appErr.HTTPStatus(), gin.H{"error": appErr.ClientResponse()})
		return
	}
	c.Status(http.StatusNoContent)
}

// =============================================================================
// PROJECT REQUEST / RESPONSE TYPES
// =============================================================================

// CreateProjectRequest is the JSON body accepted by POST /api/v1/projects.
// Money amounts (billing_rate, budget_money) are decimal pound strings for
// precision. Time values (hours_per_day, budget_hours) are "H:MM" strings.
type CreateProjectRequest struct {
	// Core fields
	ContactID              string  `json:"contact_id" binding:"required,uuid"`
	Name                   string  `json:"name" binding:"required,min=1"`
	Status                 string  `json:"status"` // defaults to "active"
	ContractPONumber       *string `json:"contract_po_number"`
	ProjectInvoiceSequence bool    `json:"project_invoice_sequence"`

	// Time and money
	Currency           string  `json:"currency"`          // defaults to "GBP"
	BudgetType         *string `json:"budget_type"`       // "hours" | "days" | "money" | nil
	BudgetHours        *string `json:"budget_hours"`      // "H:MM" — used when budget_type="hours"
	BudgetDays         *int32  `json:"budget_days"`       // integer — used when budget_type="days"
	BudgetMoney        *string `json:"budget_money"`      // pound string — used when budget_type="money"
	HoursPerDay        *string `json:"hours_per_day"`     // "H:MM" e.g. "8:00"
	BillingRate        string  `json:"billing_rate"`      // pound string e.g. "100.00"
	BillingRateUnit    *string `json:"billing_rate_unit"` // "per_hour" | "per_day"
	BillingRatePlusVAT bool    `json:"billing_rate_plus_vat"`

	// More options
	IsIR35                bool    `json:"is_ir35"`
	StartDate             *string `json:"start_date"` // "YYYY-MM-DD"
	EndDate               *string `json:"end_date"`   // "YYYY-MM-DD"
	IncludeUnbillableTime bool    `json:"include_unbillable_time"`
}

// UpdateProjectRequest mirrors CreateProjectRequest — PUT replaces all editable
// fields. contact_id is required so the project can be re-linked on edit.
type UpdateProjectRequest struct {
	ContactID              string  `json:"contact_id" binding:"required,uuid"`
	Name                   string  `json:"name" binding:"required,min=1"`
	Status                 string  `json:"status"`
	ContractPONumber       *string `json:"contract_po_number"`
	ProjectInvoiceSequence bool    `json:"project_invoice_sequence"`
	Currency               string  `json:"currency"`
	BudgetType             *string `json:"budget_type"`
	BudgetHours            *string `json:"budget_hours"`
	BudgetDays             *int32  `json:"budget_days"`
	BudgetMoney            *string `json:"budget_money"`
	HoursPerDay            *string `json:"hours_per_day"`
	BillingRate            string  `json:"billing_rate"`
	BillingRateUnit        *string `json:"billing_rate_unit"`
	BillingRatePlusVAT     bool    `json:"billing_rate_plus_vat"`
	IsIR35                 bool    `json:"is_ir35"`
	StartDate              *string `json:"start_date"`
	EndDate                *string `json:"end_date"`
	IncludeUnbillableTime  bool    `json:"include_unbillable_time"`
}

// ProjectResponse is the JSON returned for a created/fetched/updated project.
// Internal fields (pgx types, raw pence) are converted to human-readable form.
type ProjectResponse struct {
	ID                     string  `json:"id"`
	OrganisationID         string  `json:"organisation_id"`
	ContactID              string  `json:"contact_id"`
	Name                   string  `json:"name"`
	Status                 string  `json:"status"`
	ContractPONumber       *string `json:"contract_po_number"`
	ProjectInvoiceSequence bool    `json:"project_invoice_sequence"`
	Currency               string  `json:"currency"`
	BudgetType             *string `json:"budget_type"`
	BudgetHours            *string `json:"budget_hours"`  // "H:MM" when budget_type="hours"
	BudgetDays             *int32  `json:"budget_days"`   // integer when budget_type="days"
	BudgetMoney            *string `json:"budget_money"`  // pounds when budget_type="money"
	HoursPerDay            *string `json:"hours_per_day"` // "H:MM"
	BillingRate            string  `json:"billing_rate"`  // pound string
	BillingRateUnit        *string `json:"billing_rate_unit"`
	BillingRatePlusVAT     bool    `json:"billing_rate_plus_vat"`
	IsIR35                 bool    `json:"is_ir35"`
	StartDate              *string `json:"start_date"`
	EndDate                *string `json:"end_date"`
	IncludeUnbillableTime  bool    `json:"include_unbillable_time"`
	CreatedAt              string  `json:"created_at"`
	UpdatedAt              string  `json:"updated_at"`
}

// =============================================================================
// PROJECT HANDLERS
// =============================================================================

// handleListProjects handles GET /api/v1/projects — every project in the
// caller's organisation.
func (s *Server) handleListProjects(c *gin.Context) {
	list, err := s.projectService.ListProjects(c.Request.Context(), getAuthUserID(c), getAuthOrgID(c))
	if err != nil {
		appErr := AsAppError(err)
		if appErr.Code == ErrCodeInternal {
			_ = appErr.Error() // TODO: structured logger
		}
		c.JSON(appErr.HTTPStatus(), gin.H{"error": appErr.ClientResponse()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"projects": list})
}

// handleCreateProject handles POST /api/v1/projects — create one project.
// Returns 201 Created with the new project in the response body.
func (s *Server) handleCreateProject(c *gin.Context) {
	var req CreateProjectRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	project, err := s.projectService.CreateProject(c.Request.Context(), getAuthUserID(c), getAuthOrgID(c), req)
	if err != nil {
		appErr := AsAppError(err)
		if appErr.Code == ErrCodeInternal {
			_ = appErr.Error() // TODO: structured logger
		}
		c.JSON(appErr.HTTPStatus(), gin.H{"error": appErr.ClientResponse()})
		return
	}
	c.JSON(http.StatusCreated, gin.H{"project": project})
}

// handleGetProject handles GET /api/v1/projects/:id.
func (s *Server) handleGetProject(c *gin.Context) {
	project, err := s.projectService.GetProject(c.Request.Context(), getAuthUserID(c), getAuthOrgID(c), c.Param("id"))
	if err != nil {
		appErr := AsAppError(err)
		if appErr.Code == ErrCodeInternal {
			_ = appErr.Error() // TODO: structured logger
		}
		c.JSON(appErr.HTTPStatus(), gin.H{"error": appErr.ClientResponse()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"project": project})
}

// handleUpdateProject handles PUT /api/v1/projects/:id — full update.
func (s *Server) handleUpdateProject(c *gin.Context) {
	var req UpdateProjectRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	project, err := s.projectService.UpdateProject(c.Request.Context(), getAuthUserID(c), getAuthOrgID(c), c.Param("id"), req)
	if err != nil {
		appErr := AsAppError(err)
		if appErr.Code == ErrCodeInternal {
			_ = appErr.Error() // TODO: structured logger
		}
		c.JSON(appErr.HTTPStatus(), gin.H{"error": appErr.ClientResponse()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"project": project})
}

// handleDeleteProject handles DELETE /api/v1/projects/:id — hard delete.
// Returns 204 No Content on success.
func (s *Server) handleDeleteProject(c *gin.Context) {
	if err := s.projectService.DeleteProject(c.Request.Context(), getAuthUserID(c), getAuthOrgID(c), c.Param("id")); err != nil {
		appErr := AsAppError(err)
		if appErr.Code == ErrCodeInternal {
			_ = appErr.Error() // TODO: structured logger
		}
		c.JSON(appErr.HTTPStatus(), gin.H{"error": appErr.ClientResponse()})
		return
	}
	c.Status(http.StatusNoContent)
}

// =============================================================================
// MEMBER HANDLERS
// =============================================================================

// handleListMembers handles GET /api/v1/members — every member of the caller's
// organisation. The org is taken from the token; the service restricts this to
// owners/admins (a plain member gets 403).
func (s *Server) handleListMembers(c *gin.Context) {
	list, err := s.memberService.ListMembers(c.Request.Context(), getAuthUserID(c), getAuthOrgID(c))
	if err != nil {
		appErr := AsAppError(err)
		if appErr.Code == ErrCodeInternal {
			_ = appErr.Error() // TODO: structured logger
		}
		c.JSON(appErr.HTTPStatus(), gin.H{"error": appErr.ClientResponse()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"members": list})
}

// handleGetOrganisation handles GET /api/v1/organisation — the caller's
// organisation company details. The org is taken from the token; any active
// member may read (a non-member gets 403).
func (s *Server) handleGetOrganisation(c *gin.Context) {
	org, err := s.organisationService.GetOrganisation(c.Request.Context(), getAuthUserID(c), getAuthOrgID(c))
	if err != nil {
		appErr := AsAppError(err)
		if appErr.Code == ErrCodeInternal {
			_ = appErr.Error() // TODO: structured logger
		}
		c.JSON(appErr.HTTPStatus(), gin.H{"error": appErr.ClientResponse()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"organisation": org})
}

// handleUpdateOrganisation handles PUT /api/v1/organisation — update the caller's
// organisation company details. The org is taken from the token; the service
// restricts editing to owners/admins (a plain member gets 403).
func (s *Server) handleUpdateOrganisation(c *gin.Context) {
	var req UpdateOrganisationRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	org, err := s.organisationService.UpdateOrganisation(c.Request.Context(), getAuthUserID(c), getAuthOrgID(c), req)
	if err != nil {
		appErr := AsAppError(err)
		if appErr.Code == ErrCodeInternal {
			_ = appErr.Error() // TODO: structured logger
		}
		c.JSON(appErr.HTTPStatus(), gin.H{"error": appErr.ClientResponse()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"organisation": org})
}

// handleGetProfile handles GET /api/v1/profile — the caller's own "My Details".
// The user is taken from the token, so a caller can only ever read themselves.
func (s *Server) handleGetProfile(c *gin.Context) {
	profile, err := s.userService.GetProfile(c.Request.Context(), getAuthUserID(c))
	if err != nil {
		appErr := AsAppError(err)
		if appErr.Code == ErrCodeInternal {
			_ = appErr.Error() // TODO: structured logger
		}
		c.JSON(appErr.HTTPStatus(), gin.H{"error": appErr.ClientResponse()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"user": profile})
}

// handleUpdateProfile handles PUT /api/v1/profile — update the caller's first/
// last name. The user is taken from the token, so it always targets themselves.
func (s *Server) handleUpdateProfile(c *gin.Context) {
	var req UpdateProfileRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	profile, err := s.userService.UpdateProfile(c.Request.Context(), getAuthUserID(c), req)
	if err != nil {
		appErr := AsAppError(err)
		if appErr.Code == ErrCodeInternal {
			_ = appErr.Error() // TODO: structured logger
		}
		c.JSON(appErr.HTTPStatus(), gin.H{"error": appErr.ClientResponse()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"user": profile})
}
