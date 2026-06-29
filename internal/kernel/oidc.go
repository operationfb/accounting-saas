package kernel

// oidc.go (package kernel)
// =============================================================================
// RequireWorkflowOIDC — the shared middleware that gates the OIDC-protected
// /internal/v1 endpoints (consumed by external Google Cloud callers: the
// FreeAgent-push Cloud Workflow, and the Cloud Scheduler jobs that drive the FX
// rate refresh + revaluation). It validates a Google-signed OIDC identity token
// and checks the email claim matches our configured service account.
//
// It lives in the kernel (next to AuthMiddleware) because more than one domain
// needs it now (internal/integrations and internal/fxrates), and the kernel is the
// home for the cross-cutting HTTP middleware. It takes no domain dependency — only
// google's idtoken verifier — so it stays kernel-clean.
//
// This is the INVERSE of the OUTBOUND OIDC call in internal/htmlrender, and the
// same "public route, verified caller" shape as the Mailgun webhook.
// =============================================================================

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"google.golang.org/api/idtoken"
)

// RequireWorkflowOIDC authenticates a service-to-service call from a Google Cloud
// caller (Cloud Workflow / Cloud Scheduler). The caller attaches a Google-issued
// OIDC identity token for its service account; we verify it is genuinely
// Google-signed and that the email claim matches expectedServiceAccount.
//
// Fails CLOSED: when expectedServiceAccount is empty the middleware rejects every
// call (503), so an unconfigured deployment never exposes the internal endpoints.
//
// audience is intentionally not pinned (idtoken.Validate with "" skips the aud
// check): the dedicated service-account email IS the authorisation.
func RequireWorkflowOIDC(expectedServiceAccount string) gin.HandlerFunc {
	return func(c *gin.Context) {
		// Fail CLOSED when unconfigured.
		if expectedServiceAccount == "" {
			c.AbortWithStatusJSON(http.StatusServiceUnavailable, gin.H{"error": "internal endpoints are not configured"})
			return
		}

		fields := strings.Fields(c.GetHeader("Authorization"))
		if len(fields) != 2 || !strings.EqualFold(fields[0], "Bearer") {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "missing bearer token"})
			return
		}

		payload, err := idtoken.Validate(c.Request.Context(), fields[1], "")
		if err != nil {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "invalid oidc token"})
			return
		}
		email, _ := payload.Claims["email"].(string)
		verified, _ := payload.Claims["email_verified"].(bool)
		if !verified || !strings.EqualFold(email, expectedServiceAccount) {
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"error": "caller is not the authorised workflow service account"})
			return
		}

		c.Next()
	}
}
