package main

// integration_handler.go
// =============================================================================
// HTTP handlers for the FreeAgent integration's OAuth + settings endpoints.
//
// Thin boundary, same shape as the other handlers: take the caller + org from the
// token via getAuthUserID / getAuthOrgID, call IntegrationService, translate any
// error through respondError. The one odd one out is the callback, which is PUBLIC
// (a top-level browser redirect from FreeAgent, carrying no bearer token) and so
// ends in a 302 back to the SPA rather than a JSON body.
//
// Routes (registered in server.go):
//   GET    /api/v1/integrations/freeagent   → status            (owner/admin)
//   PUT    /api/v1/integrations/freeagent   → save credentials  (owner/admin)
//   DELETE /api/v1/integrations/freeagent   → disconnect        (owner/admin)
//   GET    /api/v1/freeagent/connect        → authorize URL JSON (owner/admin)
//   GET    /api/v1/freeagent/callback       → 302 back to the SPA (PUBLIC)
// =============================================================================

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

// handleGetFreeAgentStatus handles GET /api/v1/integrations/freeagent — whether
// credentials are saved and whether we're connected (no secrets returned).
func (s *Server) handleGetFreeAgentStatus(c *gin.Context) {
	status, err := s.integrationService.GetStatus(c.Request.Context(), getAuthUserID(c), getAuthOrgID(c))
	if err != nil {
		respondError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"integration": status})
}

// handleSaveFreeAgentCredentials handles PUT /api/v1/integrations/freeagent —
// store the org admin's OAuth app credentials. Returns the updated status.
func (s *Server) handleSaveFreeAgentCredentials(c *gin.Context) {
	var req SaveFreeAgentCredentialsRequest
	if !bindJSON(c, &req) {
		return
	}
	status, err := s.integrationService.SaveCredentials(c.Request.Context(), getAuthUserID(c), getAuthOrgID(c), req)
	if err != nil {
		respondError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"integration": status})
}

// handleDisconnectFreeAgent handles DELETE /api/v1/integrations/freeagent — drop
// the tokens (keep the credentials). Returns 204 No Content.
func (s *Server) handleDisconnectFreeAgent(c *gin.Context) {
	if err := s.integrationService.Disconnect(c.Request.Context(), getAuthUserID(c), getAuthOrgID(c)); err != nil {
		respondError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

// handleRepushExpense handles POST /api/v1/integrations/freeagent/expenses/:id/push
// — the manual "push this approved expense to FreeAgent again" action. It
// re-publishes the expense.approved event (owner/admin, APPROVED expenses only);
// the workflow's already_pushed guard makes re-pushing idempotent. 202 Accepted:
// the push runs asynchronously via the workflow.
func (s *Server) handleRepushExpense(c *gin.Context) {
	if err := s.expenseService.RepublishApprovedExpense(c.Request.Context(), getAuthUserID(c), getAuthOrgID(c), c.Param("id")); err != nil {
		respondError(c, err)
		return
	}
	c.Status(http.StatusAccepted)
}

// handleGetFreeAgentPushStatus handles GET /api/v1/integrations/freeagent/expenses/:id/push
// — the push outcome for one expense, used by the detail-page "Pushed ✓ / Failed ⚠"
// badge. Owner/admin only (enforced in the service); org-scoped. Sits next to the
// POST on the same path (same path, different verb): GET reads the status, POST
// re-pushes.
func (s *Server) handleGetFreeAgentPushStatus(c *gin.Context) {
	status, err := s.integrationService.GetExpensePushStatus(c.Request.Context(), getAuthUserID(c), getAuthOrgID(c), c.Param("id"))
	if err != nil {
		respondError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"push": status})
}

// handleFreeAgentConnect handles GET /api/v1/freeagent/connect — returns the
// FreeAgent authorize URL as JSON. The SPA navigates to it (window.location); this
// is deliberately not a 302 (a redirect can't carry the SPA's bearer token).
func (s *Server) handleFreeAgentConnect(c *gin.Context) {
	authorizeURL, err := s.integrationService.BuildConnectURL(c.Request.Context(), getAuthUserID(c), getAuthOrgID(c))
	if err != nil {
		respondError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"authorize_url": authorizeURL})
}

// handleFreeAgentCallback handles GET /api/v1/freeagent/callback — the PUBLIC
// endpoint FreeAgent redirects the browser to after the user approves. It carries
// no bearer token; the org is recovered from the signed `state`. The service does
// the exchange + token store and returns the SPA URL to bounce the browser to
// (success or ?freeagent=error&reason=...); any internal cause is logged here.
func (s *Server) handleFreeAgentCallback(c *gin.Context) {
	code := c.Query("code")
	state := c.Query("state")

	redirectURL, err := s.integrationService.HandleCallback(c.Request.Context(), code, state)
	if err != nil {
		// The user is still redirected back (the URL already encodes the error);
		// this only records the internal cause for ops.
		logInternalError(c, err)
	}
	c.Redirect(http.StatusFound, redirectURL)
}
