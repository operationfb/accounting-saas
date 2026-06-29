package integrations

// internal_handler.go
// =============================================================================
// The /internal/v1 HTTP surface, consumed ONLY by the external Cloud Workflow
// (never a browser). Network-reachable but gated by requireWorkflowOIDC, which
// validates a Google-signed OIDC token for our dedicated workflow service account
// — the inverse of the OUTBOUND OIDC call in html_renderer.go, same "public route,
// verified caller" shape as the Mailgun webhook.
// =============================================================================

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/operationfb/accounting-saas/internal/kernel"
)

// RegisterInternalRoutes mounts the OIDC-gated /internal/v1 endpoints. workflowSA
// is the service-account email the caller's OIDC token must match; when empty the
// endpoints reject all calls (fail closed).
func (h *Handler) RegisterInternalRoutes(r *gin.Engine, workflowSA string) {
	p := h.svc.Provider()
	internal := r.Group("/internal/v1")
	internal.Use(requireWorkflowOIDC(workflowSA))
	{
		internal.GET("/integrations/"+p+"/token", h.InternalTokenForOrg)
		internal.GET("/expenses/:id", h.InternalExpenseForPush)
		internal.GET("/expenses/:id/attachment", h.InternalExpenseAttachment)
		internal.POST("/integrations/"+p+"/push-result", h.InternalPushResult)
	}
}

// requireWorkflowOIDC authenticates a service-to-service call from the Cloud
// Workflow. It delegates to the shared kernel.RequireWorkflowOIDC (lifted there so
// internal/fxrates can reuse the same gate); this thin wrapper keeps the existing
// in-package callers/tests stable.
func requireWorkflowOIDC(expectedServiceAccount string) gin.HandlerFunc {
	return kernel.RequireWorkflowOIDC(expectedServiceAccount)
}

// internalOrgParam parses the required ?org=<uuid> query parameter.
func internalOrgParam(c *gin.Context) (uuid.UUID, bool) {
	org, err := uuid.Parse(c.Query("org"))
	if err != nil {
		kernel.RespondError(c, kernel.ErrValidation("org query parameter is required and must be a UUID", err))
		return uuid.Nil, false
	}
	return org, true
}

func (h *Handler) InternalTokenForOrg(c *gin.Context) {
	org, ok := internalOrgParam(c)
	if !ok {
		return
	}
	resp, err := h.svc.TokenForOrg(c.Request.Context(), org)
	if err != nil {
		kernel.RespondError(c, err)
		return
	}
	c.JSON(http.StatusOK, resp)
}

func (h *Handler) InternalExpenseForPush(c *gin.Context) {
	org, ok := internalOrgParam(c)
	if !ok {
		return
	}
	expenseID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		kernel.RespondError(c, kernel.ErrValidation("expense id is not a valid UUID", err))
		return
	}
	resp, err := h.svc.ExpenseForPush(c.Request.Context(), org, expenseID)
	if err != nil {
		kernel.RespondError(c, err)
		return
	}
	c.JSON(http.StatusOK, resp)
}

// InternalExpenseAttachment returns the expense's primary receipt as base64 (200),
// or 204 No Content when there's nothing to push. The workflow treats both as
// success and only adds the attachment on 200.
func (h *Handler) InternalExpenseAttachment(c *gin.Context) {
	org, ok := internalOrgParam(c)
	if !ok {
		return
	}
	expenseID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		kernel.RespondError(c, kernel.ErrValidation("expense id is not a valid UUID", err))
		return
	}
	resp, found, err := h.svc.AttachmentForPush(c.Request.Context(), org, expenseID)
	if err != nil {
		kernel.RespondError(c, err)
		return
	}
	if !found {
		c.Status(http.StatusNoContent)
		return
	}
	c.JSON(http.StatusOK, resp)
}

// internalPushResultRequest is the body for the push-result endpoint.
type internalPushResultRequest struct {
	OrganisationID     string `json:"org" binding:"required,uuid"`
	ExpenseID          string `json:"expense_id" binding:"required,uuid"`
	ExternalExpenseRef string `json:"external_expense_ref"`
	PushError          string `json:"push_error"`
}

func (h *Handler) InternalPushResult(c *gin.Context) {
	var req internalPushResultRequest
	if !kernel.BindJSON(c, &req) {
		return
	}
	org, err := uuid.Parse(req.OrganisationID)
	if err != nil {
		kernel.RespondError(c, kernel.ErrValidation("org is not a valid UUID", err))
		return
	}
	expenseID, err := uuid.Parse(req.ExpenseID)
	if err != nil {
		kernel.RespondError(c, kernel.ErrValidation("expense_id is not a valid UUID", err))
		return
	}
	if err := h.svc.RecordPushResult(c.Request.Context(), org, expenseID, req.ExternalExpenseRef, req.PushError); err != nil {
		kernel.RespondError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}
