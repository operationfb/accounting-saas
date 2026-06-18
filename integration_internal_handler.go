package main

// integration_internal_handler.go
// =============================================================================
// The /internal/v1 HTTP surface, consumed ONLY by the external Cloud Workflow
// (never by a browser). It is network-reachable but gated by requireWorkflowOIDC,
// which validates a Google-signed OIDC token for our dedicated workflow service
// account — the inverse of the existing OUTBOUND OIDC call in html_renderer.go,
// and the same "public route, verified caller" shape as the Mailgun webhook.
//
// Routes (registered in server.go, top-level, behind the middleware):
//   GET  /internal/v1/integrations/freeagent/token?org=  → access token + base URL
//   GET  /internal/v1/expenses/:id?org=                  → expense data for the push
//   POST /internal/v1/integrations/freeagent/push-result → record the outcome
// =============================================================================

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"google.golang.org/api/idtoken"
)

// requireWorkflowOIDC authenticates a service-to-service call from the Cloud
// Workflow. The workflow attaches a Google-issued OIDC identity token for its
// service account; we verify it is genuinely Google-signed and that the email
// claim matches our configured workflow service account.
//
// audience is intentionally not pinned in v1 (idtoken.Validate with "" skips the
// aud check): the dedicated workflow service-account email IS the authorisation.
// Pinning a fixed audience is a hardening step (see BACKLOG).
func requireWorkflowOIDC(expectedServiceAccount string) gin.HandlerFunc {
	return func(c *gin.Context) {
		// Fail CLOSED when unconfigured: these endpoints exist only for the
		// workflow's authenticated calls, so without a configured SA there is no
		// legitimate caller.
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

// internalOrgParam parses the required ?org=<uuid> query parameter.
func internalOrgParam(c *gin.Context) (uuid.UUID, bool) {
	org, err := uuid.Parse(c.Query("org"))
	if err != nil {
		respondError(c, ErrValidation("org query parameter is required and must be a UUID", err))
		return uuid.Nil, false
	}
	return org, true
}

// handleInternalTokenForOrg handles GET /internal/v1/integrations/freeagent/token?org=
func (s *Server) handleInternalTokenForOrg(c *gin.Context) {
	org, ok := internalOrgParam(c)
	if !ok {
		return
	}
	resp, err := s.integrationService.TokenForOrg(c.Request.Context(), org)
	if err != nil {
		respondError(c, err)
		return
	}
	c.JSON(http.StatusOK, resp)
}

// handleInternalExpenseForPush handles GET /internal/v1/expenses/:id?org=
func (s *Server) handleInternalExpenseForPush(c *gin.Context) {
	org, ok := internalOrgParam(c)
	if !ok {
		return
	}
	expenseID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		respondError(c, ErrValidation("expense id is not a valid UUID", err))
		return
	}
	resp, err := s.integrationService.ExpenseForPush(c.Request.Context(), org, expenseID)
	if err != nil {
		respondError(c, err)
		return
	}
	c.JSON(http.StatusOK, resp)
}

// internalPushResultRequest is the body for POST /internal/v1/integrations/freeagent/push-result.
type internalPushResultRequest struct {
	OrganisationID     string `json:"org" binding:"required,uuid"`
	ExpenseID          string `json:"expense_id" binding:"required,uuid"`
	ExternalExpenseRef string `json:"external_expense_ref"`
	PushError          string `json:"push_error"`
}

// handleInternalPushResult handles POST /internal/v1/integrations/freeagent/push-result.
func (s *Server) handleInternalPushResult(c *gin.Context) {
	var req internalPushResultRequest
	if !bindJSON(c, &req) {
		return
	}
	org, err := uuid.Parse(req.OrganisationID)
	if err != nil {
		respondError(c, ErrValidation("org is not a valid UUID", err))
		return
	}
	expenseID, err := uuid.Parse(req.ExpenseID)
	if err != nil {
		respondError(c, ErrValidation("expense_id is not a valid UUID", err))
		return
	}
	if err := s.integrationService.RecordPushResult(c.Request.Context(), org, expenseID, req.ExternalExpenseRef, req.PushError); err != nil {
		respondError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}
