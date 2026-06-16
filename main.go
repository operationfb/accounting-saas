package main

// main.go
// =============================================================================
// Entry point for the expense service.
//
// This file's only job is to:
//   1. Read configuration (database URL, port) from environment variables
//   2. Open the database connection pool
//   3. Build the Server and start listening
//
// Keeping main.go minimal means it's easy to understand at a glance what the
// programme does when it starts. All the real wiring lives in server.go.
// =============================================================================

import (
	"context"
	"log"
	"os"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/joho/godotenv"

	// These import paths match the `out` directories in sqlc.yaml.
	// After running `sqlc generate`, the generated files live here.
	"github.com/operationfb/accounting-saas/db/auth"
	expenses "github.com/operationfb/accounting-saas/db/expenses"
	"github.com/operationfb/accounting-saas/token"
)

func main() {
	_ = godotenv.Load()
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
	//    expenses.New(pool) is the sqlc-generated constructor. It returns a
	//    *Queries struct that wraps the pool and knows how to run every query
	//    in query.sql.go.
	//
	//    NewExpenseService wraps *Queries with business logic (validation,
	//    transactions, etc.) — defined in expense_service.go.
	//
	//    NewServer wires the service into a Gin router — defined in server.go.
	// -------------------------------------------------------------------------
	queries := expenses.New(pool)
	authQueries := auth.New(pool)
	service := NewExpenseService(pool, queries, authQueries)

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
	var emailSender EmailSender
	if smtpHost := os.Getenv("SMTP_HOST"); smtpHost != "" {
		emailSender = newSMTPSender(smtpConfig{
			Host:     smtpHost,
			Port:     envOr("SMTP_PORT", "587"),
			Username: os.Getenv("SMTP_USERNAME"),
			Password: os.Getenv("SMTP_PASSWORD"),
			From:     envOr("EMAIL_FROM", "no-reply@localhost"),
		})
		log.Printf("email: sending via SMTP host %s", smtpHost)
	} else {
		emailSender = newLogSender()
		log.Println("email: SMTP_HOST not set — emails are logged, not sent")
	}

	// Base URL of the frontend, used to build the password-reset link
	// ({APP_BASE_URL}/reset-password/<token>). Defaults to the Vite dev server.
	appBaseURL := envOr("APP_BASE_URL", "http://localhost:5173")

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

	authHandler := NewAuthHandler(authQueries, tokenMaker, accessTokenDuration, emailSender, appBaseURL, passwordResetTTL)

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
	var attachmentStorage Storage
	if bucket := os.Getenv("GCS_BUCKET"); bucket != "" {
		gcs, gcsErr := newGCSStorage(context.Background(), bucket)
		if gcsErr != nil {
			log.Fatalf("could not initialise GCS storage: %v", gcsErr)
		}
		attachmentStorage = gcs
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
	var docExtractor DocumentExtractor
	if projectID := os.Getenv("DOCAI_PROJECT_ID"); projectID != "" {
		location := envOr("DOCAI_LOCATION", "eu")
		invoiceProc := os.Getenv("DOCAI_INVOICE_PROCESSOR_ID")
		receiptProc := os.Getenv("DOCAI_EXPENSE_PROCESSOR_ID")
		if invoiceProc == "" || receiptProc == "" {
			log.Fatal("DOCAI_PROJECT_ID is set but DOCAI_INVOICE_PROCESSOR_ID and/or DOCAI_EXPENSE_PROCESSOR_ID are not")
		}
		ext, derr := newDocumentAIExtractor(context.Background(), projectID, location, invoiceProc, receiptProc)
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
	var ocrTrigger ocrEnqueuer
	if attachmentStorage != nil && docExtractor != nil {
		ocrTrigger = NewOcrService(pool, queries, attachmentStorage, docExtractor)
	}

	// 0, 0 → use the service defaults (20 MiB max file, 15-minute download URLs).
	attachmentService := NewAttachmentService(pool, queries, authQueries, attachmentStorage, ocrTrigger, 0, 0)

	// Allowed CORS origins for the browser SPA. Comma-separated in
	// CORS_ALLOWED_ORIGINS; defaults to the Nuxt dev server when unset.
	corsOrigins := parseCORSOrigins(os.Getenv("CORS_ALLOWED_ORIGINS"))

	server := NewServer(service, attachmentService, authHandler, tokenMaker, corsOrigins)

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
