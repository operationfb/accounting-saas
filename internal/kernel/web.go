package kernel

// web.go (package kernel)
// =============================================================================
// Shared HTTP/Gin layer helpers — the request/response boilerplate and the auth
// middleware every domain's handlers use. Merged here from the old root
// handler_helpers.go + auth_middleware.go so domain packages can depend on the
// kernel instead of package main.
//
//   - RespondError / BindJSON / LogInternalError — the standard error envelope
//     and request binding.
//   - AuthMiddleware + GetAuthUserID / GetAuthOrgID — bearer-token auth and the
//     accessors for the identity it stashes in the Gin context.
// =============================================================================

import (
	"log/slog"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/operationfb/accounting-saas/token"
)

// =============================================================================
// ERROR ENVELOPE + REQUEST BINDING
// =============================================================================

// RespondError writes any error to the client as the standard JSON envelope:
//
//	{ "error": { "code": "...", "message": "..." } }
//
// It is the ONE error-writer for the whole handler layer. AsAppError turns a
// plain error into an *AppError (wrapping unknown errors as ErrInternal), so the
// status code is always derived from the error itself.
//
// Internal (500) errors are the ones we didn't anticipate, so they are LOGGED
// here with the underlying cause and the request that triggered them — the only
// place a 500's real cause is recorded (it is never sent to the client).
func RespondError(c *gin.Context, err error) {
	appErr := AsAppError(err)
	if appErr.Code == ErrCodeInternal {
		LogInternalError(c, appErr.Err)
	}
	c.JSON(appErr.HTTPStatus(), gin.H{"error": appErr.ClientResponse()})
}

// LogInternalError records an unexpected (500-class) error together with the
// request that triggered it — the only place a 500's real cause is captured (it
// is never sent to the client). Shared by RespondError and the few handlers that
// must send a bespoke body but still need the cause logged (e.g. the Mailgun
// webhook, which returns a retry-friendly 500).
func LogInternalError(c *gin.Context, cause error) {
	slog.Error("request failed with internal error",
		"method", c.Request.Method,
		"path", c.Request.URL.Path,
		"err", cause,
	)
}

// BindJSON binds and validates the request body into dst. On success it returns
// true. On failure it writes a 400 in the standard error envelope (so a malformed
// request looks like every other error to the client) and returns false, letting
// the handler bail out with the idiomatic:
//
//	if !kernel.BindJSON(c, &req) {
//		return
//	}
//
// A binding failure is a *syntactic* problem (bad JSON, missing/!typed field), so
// it is 400 — deliberately distinct from a 422 business-rule ErrValidation. The
// raw binder error is kept only as the (unlogged, unexposed) cause; the client
// gets a clean generic message instead of leaked struct/validator internals.
func BindJSON(c *gin.Context, dst any) bool {
	if err := c.ShouldBindJSON(dst); err != nil {
		RespondError(c, ErrBadRequest("invalid request body", err))
		return false
	}
	return true
}

// =============================================================================
// AUTH MIDDLEWARE + IDENTITY ACCESSORS
// =============================================================================

// Context keys under which the middleware stores the authenticated identity.
const (
	ctxKeyUserID = "auth_user_id"
	ctxKeyOrgID  = "auth_org_id"
)

// AuthMiddleware returns Gin middleware that authenticates the request using a
// PASETO bearer token. Apply it to any route group that requires a logged-in
// user (e.g. the /expenses group). The /auth/login route must NOT use it — that
// is where a client obtains the token in the first place.
//
// On any failure (missing header, wrong scheme, invalid/expired token) it aborts
// the request with 401 Unauthorized — the protected handler never runs.
func AuthMiddleware(maker token.Maker) gin.HandlerFunc {
	return func(c *gin.Context) {
		// Expect: Authorization: Bearer <token>
		authHeader := c.GetHeader("Authorization")
		if authHeader == "" {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "authorization header is required"})
			return
		}

		// Split "Bearer <token>" into its two fields. strings.Fields collapses
		// runs of whitespace, so it tolerates extra spaces.
		fields := strings.Fields(authHeader)
		if len(fields) != 2 || !strings.EqualFold(fields[0], "Bearer") {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "authorization header must be 'Bearer <token>'"})
			return
		}

		// VerifyToken decrypts the token and checks expiry. Any error → 401.
		payload, err := maker.VerifyToken(fields[1])
		if err != nil {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "invalid or expired token"})
			return
		}

		// Stash the authenticated identity for downstream handlers.
		c.Set(ctxKeyUserID, payload.UserID)
		c.Set(ctxKeyOrgID, payload.OrganisationID)

		c.Next()
	}
}

// GetAuthUserID returns the authenticated user id placed in the context by
// AuthMiddleware. Only safe to call from handlers behind that middleware; if the
// key is absent it returns the zero UUID.
func GetAuthUserID(c *gin.Context) uuid.UUID {
	v, ok := c.Get(ctxKeyUserID)
	if !ok {
		return uuid.Nil
	}
	id, _ := v.(uuid.UUID)
	return id
}

// GetAuthOrgID returns the authenticated organisation id placed in the context
// by AuthMiddleware.
func GetAuthOrgID(c *gin.Context) uuid.UUID {
	v, ok := c.Get(ctxKeyOrgID)
	if !ok {
		return uuid.Nil
	}
	id, _ := v.(uuid.UUID)
	return id
}
