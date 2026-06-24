package vat

// handler.go
// =============================================================================
// The HTTP boundary for the VAT module. Like the sibling domain packages
// (organisation, banking, integrations), this Handler registers its OWN routes
// (RegisterRoutes) on the shared Gin engine from main — the root server is never
// touched. For now it exposes only the VAT Registration settings; the period
// list and the return preview/full-report land here later, alongside the
// calculation engine.
// =============================================================================

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/operationfb/accounting-saas/internal/kernel"
	"github.com/operationfb/accounting-saas/token"
)

// Handler is the HTTP boundary for the VAT endpoints.
type Handler struct {
	svc *Service
}

// NewHandler builds the Handler.
func NewHandler(svc *Service) *Handler {
	return &Handler{svc: svc}
}

// RegisterRoutes mounts the VAT routes behind bearer-token auth. The settings are
// a singleton resource (the org comes from the token), so there is no id in the
// path.
func (h *Handler) RegisterRoutes(r *gin.Engine, tokenMaker token.Maker) {
	g := r.Group("/api/v1/vat")
	g.Use(kernel.AuthMiddleware(tokenMaker))
	{
		g.GET("/settings", h.GetSettings)
		g.PUT("/settings", h.UpdateSettings)
	}
}

// GetSettings handles GET /api/v1/vat/settings — the caller's VAT registration
// settings. The org is taken from the token; any active member may read.
func (h *Handler) GetSettings(c *gin.Context) {
	res, err := h.svc.GetSettings(c.Request.Context(), kernel.GetAuthUserID(c), kernel.GetAuthOrgID(c))
	if err != nil {
		kernel.RespondError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"vat_settings": res})
}

// UpdateSettings handles PUT /api/v1/vat/settings — update the VAT settings. The
// org is taken from the token; the service restricts editing to owners/admins.
func (h *Handler) UpdateSettings(c *gin.Context) {
	var req VatSettingsRequest
	if !kernel.BindJSON(c, &req) {
		return
	}
	res, err := h.svc.UpdateSettings(c.Request.Context(), kernel.GetAuthUserID(c), kernel.GetAuthOrgID(c), req)
	if err != nil {
		kernel.RespondError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"vat_settings": res})
}
