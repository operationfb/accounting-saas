package main

// auth_middleware.go
// =============================================================================
// Authentication middleware for protected routes.
//
// It sits in front of any route group that requires a logged-in user. It reads
// the bearer token from the Authorization header, verifies it with the PASETO
// token maker, and stores the authenticated user's id + organisation id in the
// Gin request context. Downstream handlers read them with getAuthUserID /
// getAuthOrgID instead of trusting anything in the request body.
//
// On any failure (missing header, wrong scheme, invalid/expired token) it
// aborts the request with 401 Unauthorized — the protected handler never runs.
// =============================================================================

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/operationfb/accounting-saas/token"
)

// Context keys under which the middleware stores the authenticated identity.
const (
	ctxKeyUserID = "auth_user_id"
	ctxKeyOrgID  = "auth_org_id"
)

// authMiddleware returns Gin middleware that authenticates the request using a
// PASETO bearer token. Apply it to any route group that requires a logged-in
// user (e.g. the /expenses group). The /auth/login route must NOT use it — that
// is where a client obtains the token in the first place.
func authMiddleware(maker token.Maker) gin.HandlerFunc {
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

// getAuthUserID returns the authenticated user id placed in the context by
// authMiddleware. Only safe to call from handlers behind that middleware; if the
// key is absent it returns the zero UUID.
func getAuthUserID(c *gin.Context) uuid.UUID {
	v, ok := c.Get(ctxKeyUserID)
	if !ok {
		return uuid.Nil
	}
	id, _ := v.(uuid.UUID)
	return id
}

// getAuthOrgID returns the authenticated organisation id placed in the context
// by authMiddleware.
func getAuthOrgID(c *gin.Context) uuid.UUID {
	v, ok := c.Get(ctxKeyOrgID)
	if !ok {
		return uuid.Nil
	}
	id, _ := v.(uuid.UUID)
	return id
}
