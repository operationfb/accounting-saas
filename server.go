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
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/gin-contrib/cors"
	"github.com/gin-gonic/gin"

	"github.com/operationfb/accounting-saas/token"
)

// Server holds the Gin engine and all the services it needs to handle requests.
// Adding a new service module (invoices, contacts, etc.) means adding a field
// here and passing it into NewServer.
type Server struct {
	router            *gin.Engine
	attachmentService *AttachmentService
	emailInboxService *EmailInboxService
	tokenMaker        token.Maker

	// mailgunSigningKey authenticates the inbound-email webhook (HMAC). Empty
	// when the channel isn't configured, in which case the webhook isn't mounted.
	mailgunSigningKey string
}

// NewServer constructs the Server, registers all routes, and returns it.
// main.go calls this once at startup.
func NewServer(attachmentService *AttachmentService, emailInboxService *EmailInboxService, tokenMaker token.Maker, mailgunSigningKey string, corsOrigins []string) *Server {
	s := &Server{
		attachmentService: attachmentService,
		emailInboxService: emailInboxService,
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
		// Smart Upload + receipt attachments still live in package main (the
		// attachments domain hasn't been extracted yet — Stage 2). They are a
		// sub-resource of an expense, so they share the /expenses prefix and the
		// :id wildcard with the expense CRUD + reference-data routes, which are now
		// registered by internal/expenses' Handler.RegisterRoutes (from main). Two
		// groups on the same prefix is fine — Gin merges them into one route tree.
		expenses := v1.Group("/expenses")
		expenses.Use(authMiddleware(s.tokenMaker))
		{
			// POST /api/v1/expenses/capture → "Smart Upload": draft from a receipt + OCR
			expenses.POST("/capture", s.handleSmartUpload)

			// Attachments (receipt files) — a sub-resource of an expense, reusing :id.
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
