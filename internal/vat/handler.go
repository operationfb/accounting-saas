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
	svc   *Service
	fraud FraudConfig
}

// NewHandler builds the Handler. fraud carries the static HMRC fraud-prevention
// vendor identity (product/version/egress IP); the per-request bits are derived in
// fraudHeadersMiddleware.
func NewHandler(svc *Service, fraud FraudConfig) *Handler {
	return &Handler{svc: svc, fraud: fraud}
}

// RegisterRoutes mounts the VAT routes behind bearer-token auth. The settings are
// a singleton resource (the org comes from the token), so there is no id in the
// path. fraudHeadersMiddleware runs after auth (it needs the user id) and assembles
// the HMRC Gov-* headers into the request context for the HMRC-calling routes.
func (h *Handler) RegisterRoutes(r *gin.Engine, tokenMaker token.Maker) {
	g := r.Group("/api/v1/vat")
	g.Use(kernel.AuthMiddleware(tokenMaker))
	g.Use(fraudHeadersMiddleware(h.fraud))
	{
		g.GET("/settings", h.GetSettings)
		g.PUT("/settings", h.UpdateSettings)
		g.GET("/periods", h.ListPeriods)
		g.GET("/returns/:periodKey", h.GetReturn)
		g.POST("/returns/:periodKey/mark-filed", h.MarkFiled)
		g.POST("/returns/:periodKey/submit", h.SubmitReturn)
		g.GET("/hmrc/period-check", h.CheckHMRCPeriods)
		g.POST("/hmrc/period-sync", h.SyncHMRCPeriods)

		// VAT dashboard — the read layer over HMRC's MTD VAT account (any active
		// member may read; the org + VRN come from the token + settings).
		g.GET("/hmrc/obligations", h.GetHMRCObligations)
		g.GET("/hmrc/returns/:periodKey", h.GetHMRCReturn)
		g.GET("/hmrc/liabilities", h.GetHMRCLiabilities)
		g.GET("/hmrc/payments", h.GetHMRCPayments)
		g.GET("/hmrc/penalties", h.GetHMRCPenalties)
		g.GET("/hmrc/financial-details/:chargeRef", h.GetHMRCFinancialDetails)
		g.GET("/hmrc/information", h.GetHMRCInformation)
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

// CheckHMRCPeriods handles GET /api/v1/vat/hmrc/period-check — does the org's
// generated VAT period schedule line up with HMRC's obligations? Drives the
// post-connect reconciliation modal. Owner/admin only; fails open (applicable:false)
// when there's nothing to reconcile.
func (h *Handler) CheckHMRCPeriods(c *gin.Context) {
	resp, err := h.svc.CheckHMRCPeriods(c.Request.Context(), kernel.GetAuthUserID(c), kernel.GetAuthOrgID(c))
	if err != nil {
		kernel.RespondError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"period_check": resp})
}

// SyncHMRCPeriods handles POST /api/v1/vat/hmrc/period-sync — rewrite the org's
// VAT period settings to match HMRC's obligations (the modal's "Adjust to match
// HMRC" action). Owner/admin only. Returns the updated settings.
func (h *Handler) SyncHMRCPeriods(c *gin.Context) {
	resp, err := h.svc.SyncHMRCPeriods(c.Request.Context(), kernel.GetAuthUserID(c), kernel.GetAuthOrgID(c))
	if err != nil {
		kernel.RespondError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"vat_settings": resp})
}

// =============================================================================
// VAT DASHBOARD — the HMRC VAT-account read layer (see service methods in
// account.go). Each handler reads the org + VRN from the token/settings, calls
// HMRC live, and returns the mapped DTO. Any active member may read.
// =============================================================================

// GetHMRCObligations handles GET /api/v1/vat/hmrc/obligations — the org's VAT
// return periods from HMRC. Optional ?from=&to= (YYYY-MM-DD, ≤366 days) and
// ?status=O|F filters.
func (h *Handler) GetHMRCObligations(c *gin.Context) {
	res, err := h.svc.GetHMRCObligations(c.Request.Context(), kernel.GetAuthUserID(c), kernel.GetAuthOrgID(c), c.Query("from"), c.Query("to"), c.Query("status"))
	if err != nil {
		kernel.RespondError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"obligations": res})
}

// GetHMRCReturn handles GET /api/v1/vat/hmrc/returns/:periodKey — HMRC's view of a
// submitted return (the 9 boxes). An unknown periodKey is 404.
func (h *Handler) GetHMRCReturn(c *gin.Context) {
	res, err := h.svc.GetHMRCReturn(c.Request.Context(), kernel.GetAuthUserID(c), kernel.GetAuthOrgID(c), c.Param("periodKey"))
	if err != nil {
		kernel.RespondError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"hmrc_return": res})
}

// GetHMRCLiabilities handles GET /api/v1/vat/hmrc/liabilities — amounts owed to
// HMRC. Optional ?from=&to= (defaults to a trailing ~year).
func (h *Handler) GetHMRCLiabilities(c *gin.Context) {
	res, err := h.svc.GetHMRCLiabilities(c.Request.Context(), kernel.GetAuthUserID(c), kernel.GetAuthOrgID(c), c.Query("from"), c.Query("to"))
	if err != nil {
		kernel.RespondError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"liabilities": res})
}

// GetHMRCPayments handles GET /api/v1/vat/hmrc/payments — payments received by
// HMRC. Optional ?from=&to= (defaults to a trailing ~year).
func (h *Handler) GetHMRCPayments(c *gin.Context) {
	res, err := h.svc.GetHMRCPayments(c.Request.Context(), kernel.GetAuthUserID(c), kernel.GetAuthOrgID(c), c.Query("from"), c.Query("to"))
	if err != nil {
		kernel.RespondError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"payments": res})
}

// GetHMRCPenalties handles GET /api/v1/vat/hmrc/penalties — late-submission points
// + penalty charges.
func (h *Handler) GetHMRCPenalties(c *gin.Context) {
	res, err := h.svc.GetHMRCPenalties(c.Request.Context(), kernel.GetAuthUserID(c), kernel.GetAuthOrgID(c))
	if err != nil {
		kernel.RespondError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"penalties": res})
}

// GetHMRCFinancialDetails handles GET /api/v1/vat/hmrc/financial-details/:chargeRef
// — the charge breakdown for one penalty (drilled into from a penalties row).
func (h *Handler) GetHMRCFinancialDetails(c *gin.Context) {
	res, err := h.svc.GetHMRCFinancialDetails(c.Request.Context(), kernel.GetAuthUserID(c), kernel.GetAuthOrgID(c), c.Param("chargeRef"))
	if err != nil {
		kernel.RespondError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"financial_details": res})
}

// GetHMRCInformation handles GET /api/v1/vat/hmrc/information — the registered VAT
// business details.
func (h *Handler) GetHMRCInformation(c *gin.Context) {
	res, err := h.svc.GetHMRCInformation(c.Request.Context(), kernel.GetAuthUserID(c), kernel.GetAuthOrgID(c))
	if err != nil {
		kernel.RespondError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"information": res})
}
