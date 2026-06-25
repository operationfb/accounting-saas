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
		g.GET("/periods", h.ListPeriods)
		g.GET("/returns/:periodKey", h.GetReturn)
		g.POST("/returns/:periodKey/mark-filed", h.MarkFiled)
		g.POST("/returns/:periodKey/submit", h.SubmitReturn)
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

// ListPeriods handles GET /api/v1/vat/periods — the org's VAT return periods,
// generated from its settings. The org is taken from the token; any active member
// may read.
func (h *Handler) ListPeriods(c *gin.Context) {
	periods, err := h.svc.ListPeriods(c.Request.Context(), kernel.GetAuthUserID(c), kernel.GetAuthOrgID(c))
	if err != nil {
		kernel.RespondError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"periods": periods})
}

// GetReturn handles GET /api/v1/vat/returns/:periodKey — the computed return (9
// boxes + contributing lines) for the period whose end date is :periodKey. The org
// is taken from the token; any active member may read. An unknown period is 404.
func (h *Handler) GetReturn(c *gin.Context) {
	ret, err := h.svc.GetReturn(c.Request.Context(), kernel.GetAuthUserID(c), kernel.GetAuthOrgID(c), c.Param("periodKey"))
	if err != nil {
		kernel.RespondError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"vat_return": ret})
}

// MarkFiled handles POST /api/v1/vat/returns/:periodKey/mark-filed — snapshot the
// return and mark it filed, which LOCKS the period against further record changes.
// Owner/admin only.
func (h *Handler) MarkFiled(c *gin.Context) {
	ret, err := h.svc.MarkFiled(c.Request.Context(), kernel.GetAuthUserID(c), kernel.GetAuthOrgID(c), c.Param("periodKey"))
	if err != nil {
		kernel.RespondError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"vat_return": ret})
}

// SubmitReturn handles POST /api/v1/vat/returns/:periodKey/submit — submits the
// return to HMRC via Making Tax Digital, stores the HMRC form bundle number, and
// returns the HMRC acknowledgement. Owner/admin only; requires an active HMRC
// connection (GET /api/v1/integrations/hmrc must show connected: true).
func (h *Handler) SubmitReturn(c *gin.Context) {
	resp, err := h.svc.SubmitReturn(c.Request.Context(), kernel.GetAuthUserID(c), kernel.GetAuthOrgID(c), c.Param("periodKey"))
	if err != nil {
		kernel.RespondError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"submission": resp})
}
