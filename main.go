package main

// main.go
// =============================================================================
// Entry point + COMPOSITION ROOT for the accounting service.
//
// This file:
//   1. Reads configuration (database URL, port, …) from environment variables
//   2. Opens the database connection pool
//   3. Builds every domain's Service + Handler and registers their routes on the
//      shared Gin engine (the per-domain RegisterRoutes pattern)
//   4. Starts listening
//
// Post-migration, main.go is where the whole dependency graph is wired together;
// server.go is just the thin HTTP shell (engine + CORS + /health + static SPA).
// =============================================================================

import (
	"context"
	"log"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/joho/godotenv"

	// These import paths match the `out` directories in sqlc.yaml.
	// After running `sqlc generate`, the generated files live here.
	"github.com/operationfb/accounting-saas/db/auth"
	dbbanking "github.com/operationfb/accounting-saas/db/banking"
	dbbills "github.com/operationfb/accounting-saas/db/bills"
	dbcategories "github.com/operationfb/accounting-saas/db/categories"
	dbcontacts "github.com/operationfb/accounting-saas/db/contacts"
	dbcurrencies "github.com/operationfb/accounting-saas/db/currencies"
	dbemailinbox "github.com/operationfb/accounting-saas/db/email_inbox"
	dbexpenses "github.com/operationfb/accounting-saas/db/expenses"
	dbintegrations "github.com/operationfb/accounting-saas/db/integrations"
	dbinvoices "github.com/operationfb/accounting-saas/db/invoices"
	dboverview "github.com/operationfb/accounting-saas/db/overview"
	dbpayroll "github.com/operationfb/accounting-saas/db/payroll"
	dbprojects "github.com/operationfb/accounting-saas/db/projects"
	dbvat "github.com/operationfb/accounting-saas/db/vat"
	attachments "github.com/operationfb/accounting-saas/internal/attachments"
	banking "github.com/operationfb/accounting-saas/internal/banking"
	bills "github.com/operationfb/accounting-saas/internal/bills"
	categories "github.com/operationfb/accounting-saas/internal/categories"
	contacts "github.com/operationfb/accounting-saas/internal/contacts"
	currencies "github.com/operationfb/accounting-saas/internal/currencies"
	email "github.com/operationfb/accounting-saas/internal/email"
	emailinbox "github.com/operationfb/accounting-saas/internal/emailinbox"
	expenses "github.com/operationfb/accounting-saas/internal/expenses"
	htmlrender "github.com/operationfb/accounting-saas/internal/htmlrender"
	integrations "github.com/operationfb/accounting-saas/internal/integrations"
	freeagent "github.com/operationfb/accounting-saas/internal/integrations/freeagent"
	hmrc "github.com/operationfb/accounting-saas/internal/integrations/hmrc"
	invoices "github.com/operationfb/accounting-saas/internal/invoices"
	members "github.com/operationfb/accounting-saas/internal/members"
	ocr "github.com/operationfb/accounting-saas/internal/ocr"
	organisation "github.com/operationfb/accounting-saas/internal/organisation"
	overview "github.com/operationfb/accounting-saas/internal/overview"
	payroll "github.com/operationfb/accounting-saas/internal/payroll"
	projects "github.com/operationfb/accounting-saas/internal/projects"
	storage "github.com/operationfb/accounting-saas/internal/storage"
	userauth "github.com/operationfb/accounting-saas/internal/userauth"
	vat "github.com/operationfb/accounting-saas/internal/vat"
	"github.com/operationfb/accounting-saas/token"
)

func main() {
	_ = godotenv.Load()

	// Structured JSON logs to stdout: on Cloud Run these are ingested into Cloud
	// Logging as structured entries. The handler layer logs request-level internal
	// (500) errors through slog (see kernel.LogInternalError in internal/kernel); the
	// startup messages below still use the std logger.
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)))

	// -------------------------------------------------------------------------
	// 1. Read config from environment variables.
	//    Never hardcode credentials. In local development you set these in a
	//    .env file (loaded by a tool like direnv or godotenv). In production
	//    they come from GCP Secret Manager / Cloud Run environment config.
	// -------------------------------------------------------------------------
	dbURL := os.Getenv("DATABASE_URL")
	if dbURL == "" {
		// log.Fatal prints the message then calls os.Exit(1) — terminates the
		// programme immediately. Appropriate here because there's no point
		// starting without a database.
		log.Fatal("DATABASE_URL environment variable is required")
	}

	port := os.Getenv("PORT")
	if port == "" {
		port = "8080" // sensible default for local development
	}

	// -------------------------------------------------------------------------
	// 2. Open a connection pool to PostgreSQL using pgxpool.
	//
	//    Why a pool and not a single connection?
	//    An HTTP server handles many concurrent requests. Each request needs
	//    its own database connection. pgxpool manages a set of connections
	//    (default max: 4 per CPU core) and lends them out to goroutines as
	//    needed. Without a pool, concurrent requests would queue waiting for
	//    one connection.
	//
	//    context.Background() is the root context — it never cancels.
	//    We use it here because this is startup code; there's no request
	//    deadline to inherit yet.
	// -------------------------------------------------------------------------
	pool, err := pgxpool.New(context.Background(), dbURL)
	if err != nil {
		log.Fatalf("unable to connect to database: %v", err)
	}
	// defer schedules pool.Close() to run when main() returns.
	// This cleanly closes all connections when the server shuts down.
	defer pool.Close()

	// Ping the database so we fail fast at startup if the connection is wrong,
	// rather than discovering the problem on the first real request.
	if err := pool.Ping(context.Background()); err != nil {
		log.Fatalf("database ping failed: %v", err)
	}
	log.Println("database connection established")

	// -------------------------------------------------------------------------
	// 3. Build the application layer.
	//
	//    dbexpenses.New(pool) is the sqlc-generated constructor (db/expenses). It
	//    returns a *Queries struct that wraps the pool and knows how to run every
	//    query in query.sql.go.
	//
	//    expenses.NewService wraps *Queries with business logic (validation,
	//    transactions, etc.) — defined in internal/expenses.
	//
	//    Each domain's Handler then self-registers its routes on server.Router()
	//    below, after NewServer has built the engine + global middleware.
	// -------------------------------------------------------------------------
	queries := dbexpenses.New(pool)
	authQueries := auth.New(pool)
	// vatQueries is shared across domains: it backs the VAT screens AND the filed-period
	// lock (the read-only IsDateInFiledPeriod check that expenses/invoices/bills/banking
	// each call before mutating a record dated in an already-submitted VAT return).
	vatQueries := dbvat.New(pool)
	service := expenses.NewService(pool, queries, authQueries, vatQueries)

	// Contacts + Projects each have their own sqlc package + internal service and
	// self-register routes after NewServer (per-domain pattern). They share
	// cross-domain queriers, so build both query sets first, then the services:
	//   - contactSvc needs the projects querier (to refuse deleting a contact that
	//     is still referenced by a project — the soft-delete is an UPDATE, so the
	//     FK can't enforce this).
	//   - projectSvc needs the contacts querier (to validate a project's contact
	//     belongs to the same org on create/update).
	contactQueries := dbcontacts.New(pool)
	projectQueries := dbprojects.New(pool)

	contactSvc := contacts.NewService(pool, contactQueries, authQueries, projectQueries)
	projectSvc := projects.NewService(pool, projectQueries, authQueries, contactQueries)

	// Invoices: sales documents an org issues to its contacts. Like projects it
	// needs the contacts querier to validate an invoice's contact belongs to the
	// same org. Self-registers /api/v1/invoices after NewServer.
	invoiceSvc := invoices.NewService(pool, dbinvoices.New(pool), authQueries, contactQueries, vatQueries)

	// Members: the internal/members service over the auth queries for listing an
	// organisation's members and the admin User Details edit (owner/admin only).
	// Reuses the shared authQueries; the pool backs UpdateMember's transaction. Its
	// Handler self-registers /api/v1/members* after NewServer.
	memberSvc := members.NewService(pool, authQueries)

	// Payroll: the pay-run / payslip engine (owner/admin only). Reads each employee's
	// employee_payroll profile, runs the simplified PAYE/NI engine against the seeded
	// rate tables, and stores payslips per monthly pay run. Self-registers
	// /api/v1/payroll* after NewServer.
	payrollSvc := payroll.NewService(pool, dbpayroll.New(pool), authQueries)

	// Organisation: read/update the caller's own company details (the Company
	// Details settings screen). Read by any active member; edit by owner/admin.
	// Reuses the shared authQueries. Self-registers its routes after NewServer.
	organisationSvc := organisation.NewService(authQueries)

	// VAT: read/update the caller's own VAT registration settings (the "UK VAT
	// Registration" screen). Read by any active member; edit by owner/admin. The
	// settings live on the organisations table, so it reuses the shared authQueries
	// (the calculation engine + return screens land in this package later).
	// Self-registers /api/v1/vat/* after NewServer. (vatQueries was built above — it's
	// shared with the filed-period lock in expenses/invoices/bills/banking.)
	// vatSvc is constructed before hmrcIntegrationSvc (which is built in the
	// integrations block below), so the HMRC seam is injected via SetHMRC after
	// both are available. The service works with hmrc=nil (hmrc_connected=false).
	vatSvc := vat.NewService(authQueries, vatQueries, nil /* hmrc set below */)

	// User profile: read/update the caller's own "My Details" (first/last name; the
	// login email is read-only). Always self-scoped via the token, so no role check.
	// Part of internal/userauth (alongside login + password reset); self-registers
	// /api/v1/profile after NewServer.
	userSvc := userauth.NewService(authQueries)

	// -------------------------------------------------------------------------
	// Auth wiring.
	//
	// token.NewPasetoMaker builds the maker that signs/encrypts PASETO auth
	// tokens. It needs a 32-byte symmetric key from PASETO_SYMMETRIC_KEY
	// (generate one with: openssl rand -hex 16).
	// -------------------------------------------------------------------------
	symmetricKey := os.Getenv("PASETO_SYMMETRIC_KEY")
	if symmetricKey == "" {
		log.Fatal("PASETO_SYMMETRIC_KEY environment variable is required")
	}
	tokenMaker, err := token.NewPasetoMaker([]byte(symmetricKey))
	if err != nil {
		log.Fatalf("cannot create token maker: %v", err)
	}

	// How long an issued access token stays valid. Override with
	// ACCESS_TOKEN_DURATION (a Go duration string, e.g. "30m", "24h").
	accessTokenDuration := 15 * time.Minute
	if v := os.Getenv("ACCESS_TOKEN_DURATION"); v != "" {
		parsed, parseErr := time.ParseDuration(v)
		if parseErr != nil {
			log.Fatalf("invalid ACCESS_TOKEN_DURATION %q: %v", v, parseErr)
		}
		accessTokenDuration = parsed
	}

	// -------------------------------------------------------------------------
	// Email sender + password-reset config.
	//
	// EmailSender is the transport for outbound email (currently the password-
	// reset link). With SMTP_HOST set we send via SMTP; otherwise we fall back to
	// a sender that just LOGS the message, so local dev and tests work without a
	// mail server (the reset link appears in the logs).
	// -------------------------------------------------------------------------
	var emailSender email.Sender
	if smtpHost := os.Getenv("SMTP_HOST"); smtpHost != "" {
		emailSender = email.NewSMTPSender(email.Config{
			Host:     smtpHost,
			Port:     envOr("SMTP_PORT", "587"),
			Username: os.Getenv("SMTP_USERNAME"),
			Password: os.Getenv("SMTP_PASSWORD"),
			From:     envOr("EMAIL_FROM", "no-reply@localhost"),
		})
		log.Printf("email: sending via SMTP host %s", smtpHost)
	} else {
		emailSender = email.NewLogSender()
		log.Println("email: SMTP_HOST not set — emails are logged, not sent")
	}

	// Base URL of the frontend, used to build the password-reset link
	// ({APP_BASE_URL}/reset-password/<token>) and the OAuth callback landing.
	// Defaults to the production site so a deployment that omits APP_BASE_URL
	// degrades to the live host rather than localhost; local dev sets it in .env.
	appBaseURL := envOr("APP_BASE_URL", "https://kontala.com")

	// How long a password-reset link stays valid. Override with PASSWORD_RESET_TTL
	// (a Go duration string, e.g. "15m", "1h").
	passwordResetTTL := 15 * time.Minute
	if v := os.Getenv("PASSWORD_RESET_TTL"); v != "" {
		parsed, parseErr := time.ParseDuration(v)
		if parseErr != nil {
			log.Fatalf("invalid PASSWORD_RESET_TTL %q: %v", v, parseErr)
		}
		passwordResetTTL = parsed
	}

	// Login + password reset (internal/userauth). emailSender is the root concrete
	// transport (smtpSender/logSender); it satisfies the userauth.EmailSender seam.
	authHandler := userauth.NewAuthHandler(authQueries, tokenMaker, accessTokenDuration, emailSender, appBaseURL, passwordResetTTL)

	// -------------------------------------------------------------------------
	// Attachment storage (Google Cloud Storage).
	//
	// Receipt FILES live in GCS; their METADATA lives in Postgres. GCS_BUCKET
	// names the bucket. Credentials come from Application Default Credentials
	// (locally: `gcloud auth application-default login`; on Cloud Run: the
	// attached service account). When GCS_BUCKET is unset we start without
	// attachment support so the rest of the API still runs in environments that
	// have no GCS configured (e.g. a frontend dev's machine).
	// -------------------------------------------------------------------------
	var attachmentStorage storage.Storage
	if bucket := os.Getenv("GCS_BUCKET"); bucket != "" {
		gcsStore, gcsErr := storage.NewGCS(context.Background(), bucket)
		if gcsErr != nil {
			log.Fatalf("could not initialise GCS storage: %v", gcsErr)
		}
		attachmentStorage = gcsStore
		log.Printf("attachment storage: GCS bucket %q", bucket)
	} else {
		log.Println("attachment storage: GCS_BUCKET not set — receipt uploads are disabled")
	}

	// -------------------------------------------------------------------------
	// OCR extraction (Google Document AI) — "Smart Upload".
	//
	// Optional, exactly like GCS above. Requires DOCAI_PROJECT_ID, DOCAI_LOCATION
	// (the multi-region — use 'eu' for UK/EU data residency), and BOTH processor
	// ids (the Invoice Parser for invoices, the Expense Parser for receipts).
	// When unset, Smart Upload still creates draft expenses; they just stay
	// ocr_status=PENDING with no extraction.
	// -------------------------------------------------------------------------
	var docExtractor ocr.DocumentExtractor
	if projectID := os.Getenv("DOCAI_PROJECT_ID"); projectID != "" {
		location := envOr("DOCAI_LOCATION", "eu")
		invoiceProc := os.Getenv("DOCAI_INVOICE_PROCESSOR_ID")
		receiptProc := os.Getenv("DOCAI_EXPENSE_PROCESSOR_ID")
		if invoiceProc == "" || receiptProc == "" {
			log.Fatal("DOCAI_PROJECT_ID is set but DOCAI_INVOICE_PROCESSOR_ID and/or DOCAI_EXPENSE_PROCESSOR_ID are not")
		}
		ext, derr := ocr.NewDocumentAIExtractor(context.Background(), projectID, location, invoiceProc, receiptProc)
		if derr != nil {
			log.Fatalf("could not initialise Document AI: %v", derr)
		}
		docExtractor = ext
		log.Printf("OCR: Document AI in location %q (invoice + expense parsers)", location)
	} else {
		log.Println("OCR: DOCAI_PROJECT_ID not set — Smart Upload creates drafts without OCR")
	}

	// The background OCR worker needs BOTH storage (to re-read the file) and an
	// extractor. Wire it in only when both exist; otherwise leave it nil so the
	// attachment service treats OCR as disabled (a typed-nil interface is avoided
	// by assigning only inside this guard).
	var ocrTrigger attachments.OcrEnqueuer
	if attachmentStorage != nil && docExtractor != nil {
		ocrTrigger = ocr.NewOcrService(pool, queries, attachmentStorage, docExtractor, attachments.PlaceholderDescription)
	}

	// 0, 0 → use the service defaults (20 MiB max file, 15-minute download URLs).
	attachmentService := attachments.NewService(pool, queries, authQueries, attachmentStorage, ocrTrigger, 0, 0)

	// -------------------------------------------------------------------------
	// Email-to-expense (Mailgun inbound webhook).
	//
	// Optional, like GCS/OCR above. Independent switches:
	//   - INBOX_DOMAIN                 enables per-(user,org) receipt addresses
	//                                  and the channel itself.
	//   - MAILGUN_INBOUND_SIGNING_KEY  authenticates the inbound webhook (HMAC);
	//                                  without it the address still displays but
	//                                  the webhook route isn't mounted.
	//   - GOTENBERG_URL                enables HTML-body receipts (render → PDF).
	// Everything captured lands in OUR Postgres + GCS via the attachment service.
	// -------------------------------------------------------------------------
	var htmlRenderer htmlrender.Renderer
	if gotenbergURL := os.Getenv("GOTENBERG_URL"); gotenbergURL != "" {
		htmlRenderer = htmlrender.NewGotenberg(gotenbergURL)
		log.Printf("email inbox: HTML-body rendering via Gotenberg at %s", gotenbergURL)
	} else {
		log.Println("email inbox: GOTENBERG_URL not set — HTML-body receipts are skipped")
	}

	mailgunSigningKey := os.Getenv("MAILGUN_INBOUND_SIGNING_KEY")
	var emailInboxService *emailinbox.Service
	if inboxDomain := os.Getenv("INBOX_DOMAIN"); inboxDomain != "" {
		emailInboxService = emailinbox.NewService(authQueries, dbemailinbox.New(pool), attachmentService, htmlRenderer, inboxDomain)
		if mailgunSigningKey == "" {
			log.Printf("email inbox: INBOX_DOMAIN=%q set but MAILGUN_INBOUND_SIGNING_KEY empty — address display works, inbound webhook disabled", inboxDomain)
		} else {
			log.Printf("email inbox: enabled for domain %q", inboxDomain)
		}
	} else {
		log.Println("email inbox: INBOX_DOMAIN not set — email-to-expense disabled")
	}

	// -------------------------------------------------------------------------
	// FreeAgent integration (push approved expenses out, via the external Cloud
	// Workflow). The monolith's half is OAuth credential/token custody + the
	// one-time interactive connect flow — it does NOT push or map fields. Unlike
	// GCS/OCR this is ALWAYS wired: the client holds no credentials at startup
	// (the GLOBAL app credentials live in the provider_credentials table, managed
	// directly in the DB), and FREEAGENT_SANDBOX only chooses which FreeAgent host
	// the client talks to.
	// -------------------------------------------------------------------------
	integrationQueries := dbintegrations.New(pool)
	// apiPublicURL is OUR backend's externally reachable base URL — it builds the
	// OAuth redirect_uri the provider sends the browser back to (the BACKEND, distinct
	// from the frontend appBaseURL). Defaults to the production host so a deployment
	// that omits API_PUBLIC_URL degrades to the live host rather than localhost;
	// local dev sets it in .env.
	apiPublicURL := envOr("API_PUBLIC_URL", "https://app.kontala.com")

	freeAgentSandbox := os.Getenv("FREEAGENT_SANDBOX") == "true"
	faClient := freeagent.NewClient(freeAgentSandbox)
	integrationSvc := integrations.NewService(integrationQueries, authQueries, faClient, attachmentService, freeagent.MaxAttachmentBytes, freeagent.ProviderKey, tokenMaker, apiPublicURL, appBaseURL)
	integrationHandler := integrations.NewHandler(integrationSvc, service)
	log.Printf("FreeAgent integration: enabled (sandbox=%v, redirect_uri=%s/api/v1/%s/callback)", freeAgentSandbox, strings.TrimRight(apiPublicURL, "/"), freeagent.ProviderKey)

	// HMRC Making Tax Digital — OAuth connect + token storage (same pattern as
	// FreeAgent). The provider_credentials row (provider='hmrc') must exist in the
	// DB; HMRC_SANDBOX=true points the client at the sandbox host. Submission is
	// synchronous in-process (no Pub/Sub/Workflow needed — HMRC responds with a form
	// bundle number immediately). Credentials already in DB via provider_credentials.
	hmrcSandbox := os.Getenv("HMRC_SANDBOX") == "true"
	hmrcClient := hmrc.NewClient(hmrcSandbox)
	hmrcIntegrationSvc := integrations.NewService(integrationQueries, authQueries, hmrcClient, nil, 0, hmrc.ProviderKey, tokenMaker, apiPublicURL, appBaseURL)
	hmrcIntegrationHandler := integrations.NewHandler(hmrcIntegrationSvc, service)
	log.Printf("HMRC MTD integration: enabled (sandbox=%v, redirect_uri=%s/api/v1/%s/callback)", hmrcSandbox, strings.TrimRight(apiPublicURL, "/"), hmrc.ProviderKey)

	// Wire the HMRC seam into the VAT service now that both are built.
	// This is the same late-inject pattern as service.SetPublisher above.
	vatSvc.SetHMRC(hmrcIntegrationSvc)

	// -------------------------------------------------------------------------
	// Pub/Sub publisher for the "expense.approved" event (the trigger that drives
	// the FreeAgent push via Eventarc → Cloud Workflow). Optional + nil-guarded
	// like GCS/OCR: with PUBSUB_EXPENSE_APPROVED_TOPIC unset, ExpenseService's
	// publisher stays nil and approvals simply don't publish. Credentials come from
	// ADC (like GCS); GOOGLE_CLOUD_PROJECT may be empty (auto-detected). The topic
	// must already exist (provisioned out-of-band — see the plan's infra section).
	// -------------------------------------------------------------------------
	if topicID := os.Getenv("PUBSUB_EXPENSE_APPROVED_TOPIC"); topicID != "" {
		pub, pubErr := expenses.NewPubSubPublisher(context.Background(), os.Getenv("GOOGLE_CLOUD_PROJECT"), topicID)
		if pubErr != nil {
			log.Fatalf("could not initialise Pub/Sub publisher: %v", pubErr)
		}
		service.SetPublisher(pub)
		log.Printf("expense-approved events: publishing to Pub/Sub topic %q", topicID)
	} else {
		log.Println("expense-approved events: PUBSUB_EXPENSE_APPROVED_TOPIC not set — publishing disabled")
	}

	// The email of the Cloud Workflow's service account. The /internal/v1 endpoints
	// accept only OIDC tokens for this identity; unset → those endpoints fail closed
	// (the workflow's authenticated calls are the only legitimate ones).
	workflowServiceAccount := os.Getenv("WORKFLOW_SERVICE_ACCOUNT")
	if workflowServiceAccount == "" {
		log.Println("internal endpoints: WORKFLOW_SERVICE_ACCOUNT not set — /internal/v1 rejects all calls (set it in production)")
	}

	// Allowed CORS origins for the browser SPA. Comma-separated in
	// CORS_ALLOWED_ORIGINS; defaults to the Nuxt dev server when unset.
	corsOrigins := parseCORSOrigins(os.Getenv("CORS_ALLOWED_ORIGINS"))

	server := NewServer(corsOrigins)

	// Every domain self-registers its routes on the shared engine, like every other
	// domain. Attachments mounts the receipt sub-resource + Smart Upload under
	// /api/v1/expenses (same :id wildcard as the expense CRUD — Gin merges the two
	// groups). Email-inbox always mounts /inbox-address (it reports enabled:false
	// when emailInboxService is nil); its webhook mounts only when configured.
	expenses.NewHandler(service).RegisterRoutes(server.Router(), tokenMaker)
	attachments.NewHandler(attachmentService).RegisterRoutes(server.Router(), tokenMaker)
	emailinbox.NewHandler(emailInboxService, mailgunSigningKey).RegisterRoutes(server.Router(), tokenMaker)

	// Each integration registers its OWN routes on the shared engine (the
	// per-domain pattern) — after NewServer so the global middleware (CORS) is in
	// place. Adding a provider is another NewService/NewHandler + these two calls.
	integrationHandler.RegisterRoutes(server.Router(), tokenMaker)
	hmrcIntegrationHandler.RegisterRoutes(server.Router(), tokenMaker)
	integrationHandler.RegisterInternalRoutes(server.Router(), workflowServiceAccount)

	// Currencies: a thin read-only service over the GLOBAL ISO 4217 reference
	// table. Like the integration handler it registers its own route on the shared
	// engine (the per-domain pattern), behind bearer-token auth. No org scoping —
	// the list is universal.
	currencyHandler := currencies.NewHandler(currencies.NewService(dbcurrencies.New(pool)))
	currencyHandler.RegisterRoutes(server.Router(), tokenMaker)

	// Banking: the org's own bank accounts + the explain/reconcile flow. Its service
	// takes the categories query set (the explain reference lookups + VAT). Registers
	// its own routes on the shared engine (the per-domain pattern).
	categoryQueries := dbcategories.New(pool)
	// dbinvoices.New(pool) is the cross-domain dependency for the Invoice Receipt
	// explanation: validate the target invoice + keep its paid_value_minor in sync.
	bankingHandler := banking.NewHandler(banking.NewService(pool, dbbanking.New(pool), authQueries, categoryQueries, dbinvoices.New(pool), dbbills.New(pool), vatQueries))
	bankingHandler.RegisterRoutes(server.Router(), tokenMaker)

	// Overview: the read-only dashboard (FreeAgent-style landing page). A
	// cross-domain summary like the VAT module — its service takes the shared
	// authQueries (authorisation) + its own db/overview read queries. First card is
	// Cashflow (from bank_transactions); more cards add sibling GET routes.
	overview.NewHandler(overview.NewService(authQueries, dboverview.New(pool))).RegisterRoutes(server.Router(), tokenMaker)

	// Categories: the reconcile reference endpoints (the explain Type dropdown + its
	// per-type category picker), a thin read-only service over the categories queries.
	categories.NewHandler(categories.NewService(categoryQueries, authQueries)).RegisterRoutes(server.Router(), tokenMaker)

	// Bills: accounts-payable supplier invoices (the payable twin of invoices). Like
	// invoices it validates its contact against the org; it also validates the spending
	// category + VAT rate (the categories querier, which also serves GetVatRate) and the
	// optional project link. Self-registers /api/v1/bills + /api/v1/bill-categories.
	billSvc := bills.NewService(pool, dbbills.New(pool), authQueries, contactQueries, projectQueries, categoryQueries, vatQueries)
	bills.NewHandler(billSvc).RegisterRoutes(server.Router(), tokenMaker)

	// Auth (login + password reset, PUBLIC routes) + the user profile, plus Contacts
	// + Projects + Members + Organisation — each self-registering on the shared engine.
	authHandler.RegisterRoutes(server.Router())
	userauth.NewHandler(userSvc).RegisterRoutes(server.Router(), tokenMaker)
	contacts.NewHandler(contactSvc).RegisterRoutes(server.Router(), tokenMaker)
	projects.NewHandler(projectSvc).RegisterRoutes(server.Router(), tokenMaker)
	invoices.NewHandler(invoiceSvc).RegisterRoutes(server.Router(), tokenMaker)
	members.NewHandler(memberSvc).RegisterRoutes(server.Router(), tokenMaker)
	payroll.NewHandler(payrollSvc).RegisterRoutes(server.Router(), tokenMaker)
	organisationHandler := organisation.NewHandler(organisationSvc)
	organisationHandler.RegisterRoutes(server.Router(), tokenMaker)
	// HMRC fraud-prevention vendor identity (static half of the Gov-* headers; the
	// per-request half is derived in the VAT fraud middleware). HMRC_VENDOR_PUBLIC_IP
	// is our Cloud Run static-egress IP — blank until provisioned, in which case the
	// vendor-IP-derived headers are omitted rather than sent wrong.
	fraudConfig := vat.FraudConfig{
		ProductName:      envOr("HMRC_VENDOR_PRODUCT_NAME", "Kontala"),
		Version:          envOr("HMRC_VENDOR_VERSION", "0.0.0"),
		VendorPublicIP:   os.Getenv("HMRC_VENDOR_PUBLIC_IP"),
		ConnectionMethod: envOr("HMRC_CONNECTION_METHOD", "WEB_APP_VIA_SERVER"),
	}
	vat.NewHandler(vatSvc, fraudConfig).RegisterRoutes(server.Router(), tokenMaker)

	// Serve the built Vue SPA from the same origin as the API when WEB_DIST_DIR is
	// set. The container image bakes WEB_DIST_DIR=/web (the copied web/dist); locally
	// it is unset, so the API runs on its own and the SPA is served by the Vite dev
	// server as before. Same-origin serving means the browser calls /api/v1 with a
	// relative URL — no CORS involved.
	if dir := strings.TrimSpace(os.Getenv("WEB_DIST_DIR")); dir != "" {
		server.enableStaticSPA(dir)
		log.Printf("serving SPA from %s", dir)
	}

	// -------------------------------------------------------------------------
	// 4. Start the HTTP server.
	// -------------------------------------------------------------------------
	log.Printf("server listening on :%s", port)
	if err := server.Run(":" + port); err != nil {
		log.Fatalf("server error: %v", err)
	}
}

// parseCORSOrigins turns a comma-separated CORS_ALLOWED_ORIGINS value into a
// slice of trimmed origins. When empty it defaults to the local Nuxt dev server
// so the app works out of the box in development.
func parseCORSOrigins(raw string) []string {
	const devDefault = "http://localhost:3000"
	if strings.TrimSpace(raw) == "" {
		return []string{devDefault}
	}
	var origins []string
	for _, o := range strings.Split(raw, ",") {
		if o = strings.TrimSpace(o); o != "" {
			origins = append(origins, o)
		}
	}
	if len(origins) == 0 {
		return []string{devDefault}
	}
	return origins
}

// envOr returns the trimmed value of environment variable key, or def when it
// is unset or blank.
func envOr(key, def string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return def
}
