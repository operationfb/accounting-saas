package payroll

// handler.go
// =============================================================================
// HTTP boundary for the payroll endpoints. Like the sibling domains, this Handler
// registers its OWN routes (RegisterRoutes) on the shared Gin engine from main, all
// behind bearer-token auth; the caller's identity comes from the token. Every
// payroll action is owner/admin only (enforced in the service).
// =============================================================================

import (
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"

	"github.com/operationfb/accounting-saas/internal/kernel"
	"github.com/operationfb/accounting-saas/token"
)

// Handler is the HTTP boundary for the payroll endpoints.
type Handler struct {
	svc *Service
}

// NewHandler builds the Handler.
func NewHandler(svc *Service) *Handler {
	return &Handler{svc: svc}
}

// RegisterRoutes mounts the payroll routes behind bearer-token auth.
func (h *Handler) RegisterRoutes(r *gin.Engine, tokenMaker token.Maker) {
	g := r.Group("/api/v1/payroll")
	g.Use(kernel.AuthMiddleware(tokenMaker))
	{
		// GET    /api/v1/payroll/overview          → status + YTD + history + employees
		// GET    /api/v1/payroll/periods           → the year's pay runs (history list)
		// POST   /api/v1/payroll/periods           → prepare a draft run for a month
		// GET    /api/v1/payroll/periods/:id       → one run + its payslips
		// POST   /api/v1/payroll/periods/:id/complete → "Run & Report" (finalise)
		// DELETE /api/v1/payroll/periods/:id       → delete the latest run
		// GET    /api/v1/payroll/payslips/:id      → one payslip
		// PUT    /api/v1/payroll/payslips/:id      → edit a payslip (recompute)
		g.GET("/overview", h.GetOverview)
		g.GET("/periods", h.ListPeriods)
		g.POST("/periods", h.PreparePayRun)
		g.GET("/periods/:id", h.GetPayRun)
		g.POST("/periods/:id/complete", h.CompletePayRun)
		g.DELETE("/periods/:id", h.DeletePayRun)
		g.GET("/payslips/:id", h.GetPayslip)
		g.PUT("/payslips/:id", h.UpdatePayslip)
	}
}

// taxYearParam reads an optional ?tax_year= query param.
func taxYearParam(c *gin.Context) *int {
	raw := c.Query("tax_year")
	if raw == "" {
		return nil
	}
	n, err := strconv.Atoi(raw)
	if err != nil {
		return nil
	}
	return &n
}

// GetOverview handles GET /api/v1/payroll/overview.
func (h *Handler) GetOverview(c *gin.Context) {
	ov, err := h.svc.GetOverview(c.Request.Context(), kernel.GetAuthUserID(c), kernel.GetAuthOrgID(c), taxYearParam(c))
	if err != nil {
		kernel.RespondError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"overview": ov})
}

// ListPeriods handles GET /api/v1/payroll/periods.
func (h *Handler) ListPeriods(c *gin.Context) {
	list, err := h.svc.ListPayRuns(c.Request.Context(), kernel.GetAuthUserID(c), kernel.GetAuthOrgID(c), taxYearParam(c))
	if err != nil {
		kernel.RespondError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"pay_runs": list})
}

// PreparePayRun handles POST /api/v1/payroll/periods. Returns 201 Created.
func (h *Handler) PreparePayRun(c *gin.Context) {
	var req PreparePayRunRequest
	if !kernel.BindJSON(c, &req) {
		return
	}
	run, err := h.svc.PreparePayRun(c.Request.Context(), kernel.GetAuthUserID(c), kernel.GetAuthOrgID(c), req)
	if err != nil {
		kernel.RespondError(c, err)
		return
	}
	c.JSON(http.StatusCreated, gin.H{"pay_run": run})
}

// GetPayRun handles GET /api/v1/payroll/periods/:id.
func (h *Handler) GetPayRun(c *gin.Context) {
	run, err := h.svc.GetPayRun(c.Request.Context(), kernel.GetAuthUserID(c), kernel.GetAuthOrgID(c), c.Param("id"))
	if err != nil {
		kernel.RespondError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"pay_run": run})
}

// CompletePayRun handles POST /api/v1/payroll/periods/:id/complete.
func (h *Handler) CompletePayRun(c *gin.Context) {
	run, err := h.svc.CompletePayRun(c.Request.Context(), kernel.GetAuthUserID(c), kernel.GetAuthOrgID(c), c.Param("id"))
	if err != nil {
		kernel.RespondError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"pay_run": run})
}

// DeletePayRun handles DELETE /api/v1/payroll/periods/:id. Returns 204 No Content.
func (h *Handler) DeletePayRun(c *gin.Context) {
	if err := h.svc.DeletePayRun(c.Request.Context(), kernel.GetAuthUserID(c), kernel.GetAuthOrgID(c), c.Param("id")); err != nil {
		kernel.RespondError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

// GetPayslip handles GET /api/v1/payroll/payslips/:id.
func (h *Handler) GetPayslip(c *gin.Context) {
	ps, err := h.svc.GetPayslip(c.Request.Context(), kernel.GetAuthUserID(c), kernel.GetAuthOrgID(c), c.Param("id"))
	if err != nil {
		kernel.RespondError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"payslip": ps})
}

// UpdatePayslip handles PUT /api/v1/payroll/payslips/:id.
func (h *Handler) UpdatePayslip(c *gin.Context) {
	var req UpdatePayslipRequest
	if !kernel.BindJSON(c, &req) {
		return
	}
	ps, err := h.svc.UpdatePayslip(c.Request.Context(), kernel.GetAuthUserID(c), kernel.GetAuthOrgID(c), c.Param("id"), req)
	if err != nil {
		kernel.RespondError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"payslip": ps})
}
