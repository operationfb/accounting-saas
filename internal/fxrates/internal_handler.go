package fxrates

// internal_handler.go
// =============================================================================
// The /internal/v1 refresh endpoint, consumed ONLY by a daily Cloud Scheduler job
// (never a browser). Network-reachable but gated by kernel.RequireWorkflowOIDC,
// which validates a Google-signed OIDC token for our configured service account —
// the same "public route, verified caller" gate the FreeAgent-push internal
// endpoints use. Cloud Run scales to zero, so an in-process timer can't be relied
// on; an external scheduler hitting this endpoint is the durable trigger.
//
//   POST /internal/v1/fxrates/refresh?on=YYYY-MM-DD   (on optional, defaults today)
// =============================================================================

import (
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/operationfb/accounting-saas/internal/kernel"
)

// RegisterInternalRoutes mounts the OIDC-gated refresh endpoint. workflowSA is the
// service-account email the caller's OIDC token must match; empty ⇒ reject all
// (fail closed), matching internal/integrations.
func (h *Handler) RegisterInternalRoutes(r *gin.Engine, workflowSA string) {
	internal := r.Group("/internal/v1")
	internal.Use(kernel.RequireWorkflowOIDC(workflowSA))
	{
		internal.POST("/fxrates/refresh", h.InternalRefresh)
	}
}

// InternalRefresh pulls the day's rates from the provider and stores them. Returns
// the number of rates written. A nil/unconfigured provider stores 0 and still 200s
// (the job is a no-op, not a failure).
func (h *Handler) InternalRefresh(c *gin.Context) {
	on, ok := parseOn(c)
	if !ok {
		return
	}
	// RefreshRates upserts the day's rates and (when a revaluer is wired) chains the
	// unrealised-FX revaluation against them — so this endpoint and the startup best-effort
	// fetch keep the 391 accruals in sync the same way.
	count, err := h.svc.RefreshRates(c.Request.Context(), on)
	if err != nil {
		kernel.RespondError(c, err)
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"refreshed":  count,
		"rate_date":  on.Format("2006-01-02"),
		"fetched_at": time.Now().UTC().Format(time.RFC3339),
	})
}
