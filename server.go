package main

// server.go
// =============================================================================
// The thin HTTP shell. Post-migration this file owns NO domain logic and NO
// domain routes — every API route is registered by its internal/<domain> Handler
// on Router() from main. What remains here is the engine itself:
//   - Create + configure the Gin engine (global CORS, multipart limit)
//   - The /health liveness probe
//   - The static-SPA fallback (enableStaticSPA)
//   - Router() — the per-domain RegisterRoutes seam
//
// All business logic lives in internal/<domain>; shared HTTP/error helpers in
// internal/kernel. Handlers and DTOs no longer live here.
// =============================================================================

import (
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/gin-contrib/cors"
	"github.com/gin-gonic/gin"

	kernel "github.com/operationfb/accounting-saas/internal/kernel"
)

// Server is now a thin shell: it owns the Gin engine + the global middleware
// (CORS), exposes /health and the static SPA, and hands its Router() to each
// domain package so they register their OWN routes from main. It holds NO domain
// services anymore — the modular-monolith migration is complete.
type Server struct {
	router *gin.Engine
}

// NewServer builds the engine, applies global middleware, and registers the only
// route this file still owns (/health). Domain routes are registered by their
// packages on Router() from main; the static SPA is wired by enableStaticSPA.
func NewServer(corsOrigins []string) *Server {
	s := &Server{}

	// gin.Default() creates a Gin engine with two built-in middleware:
	//   - Logger: prints each request (method, path, status, latency) to stdout
	//   - Recovery: catches panics and returns 500 instead of crashing the server
	// For production you'd replace these with structured logging middleware,
	// but Default() is the right starting point.
	s.router = gin.Default()

	// Trust only well-known proxy ranges so c.ClientIP() reflects the REAL client IP
	// (not a spoofed X-Forwarded-For). This matters for the HMRC fraud-prevention
	// Gov-Client-Public-IP header. Cloud Run terminates TLS at Google's front end and
	// forwards via private ranges; trusting those (plus loopback for local dev) makes
	// the right-most untrusted XFF entry the client. Errors here are non-fatal — fall
	// back to Gin's default (trust all) with a warning rather than refusing to boot.
	if err := s.router.SetTrustedProxies([]string{
		"127.0.0.1/8", "::1/128", // local dev
		"169.254.0.0/16",                              // GCP metadata / link-local
		"10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16", // private ranges (Cloud Run front end)
	}); err != nil {
		log.Printf("warning: could not set trusted proxies, client IP may be unreliable: %v", err)
	}

	// How much of a multipart upload Gin buffers in memory before spilling the
	// rest to a temp file. We stream uploads to GCS and hard-cap the body in the
	// handler, so a modest in-memory buffer is plenty.
	s.router.MaxMultipartMemory = 8 << 20 // 8 MiB

	// CORS must be registered globally and BEFORE the routes/auth middleware.
	// A browser sends a preflight OPTIONS request (with no Authorization header)
	// before any cross-origin call that carries the bearer token; CORS has to
	// answer that preflight (204) before kernel.AuthMiddleware can reject it for the
	// missing token. Registering here (before registerRoutes) also puts CORS in
	// Gin's NoRoute/NoMethod chains so bare OPTIONS preflights are handled.
	s.router.Use(cors.New(cors.Config{
		AllowOrigins:     corsOrigins,
		AllowMethods:     []string{"GET", "POST", "PUT", "PATCH", "DELETE", "OPTIONS"},
		AllowHeaders:     []string{"Origin", "Content-Type", "Authorization", "Accept", "X-Client-Fraud-Signals"},
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
			kernel.RespondError(c, &kernel.AppError{Code: kernel.ErrCodeNotFound, Message: "resource not found"})
			return
		}

		// The SPA is a GET/HEAD surface; any other method on an unknown path is a 404.
		if c.Request.Method != http.MethodGet && c.Request.Method != http.MethodHead {
			kernel.RespondError(c, &kernel.AppError{Code: kernel.ErrCodeNotFound, Message: "resource not found"})
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

// registerRoutes declares the only routes this shell still owns: the liveness
// probe. Every API route is registered by its domain package on Router() from
// main (expenses, attachments, emailinbox, integrations, contacts, …).
func (s *Server) registerRoutes() {
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
}
