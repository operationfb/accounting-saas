package overview

// handler.go
// =============================================================================
// The HTTP boundary for the Overview dashboard. Like the sibling domain packages
// (vat, banking, organisation), this Handler registers its OWN routes
// (RegisterRoutes) on the shared Gin engine from main — the root server is never
// touched. The dashboard is a singleton resource (the org comes from the token,
// so there is no id in the path); every endpoint is a read for any active member.
// More cards (Banking, Invoice Timeline, Expenses/Bills) add sibling GET routes
// here over time.
// =============================================================================

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/operationfb/accounting-saas/internal/kernel"
	"github.com/operationfb/accounting-saas/token"
)

// Handler is the HTTP boundary for the Overview endpoints.
type Handler struct {
	svc *Service
}

// NewHandler builds the Handler.
func NewHandler(svc *Service) *Handler {
	return &Handler{svc: svc}
}

// RegisterRoutes mounts the Overview routes behind bearer-token auth. The org
// comes from the token, so there is no id in the path.
func (h *Handler) RegisterRoutes(r *gin.Engine, tokenMaker token.Maker) {
	g := r.Group("/api/v1/overview")
	g.Use(kernel.AuthMiddleware(tokenMaker))
	{
		g.GET("/cashflow", h.GetCashflow)
		g.GET("/invoice-timeline", h.GetInvoiceTimeline)
		g.GET("/banking", h.GetBanking)
	}
}

// GetCashflow handles GET /api/v1/overview/cashflow — the Cashflow card: money in
// vs money out per month over the last 12 months, plus the window totals and the
// net Balance. The org/user come from the token; any active member may read.
func (h *Handler) GetCashflow(c *gin.Context) {
	res, err := h.svc.Cashflow(c.Request.Context(), kernel.GetAuthUserID(c), kernel.GetAuthOrgID(c))
	if err != nil {
		kernel.RespondError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"cashflow": res})
}

// GetInvoiceTimeline handles GET /api/v1/overview/invoice-timeline — the Invoice
// Timeline card: SENT invoices' totals per month split into Overdue/Due/Paid, plus
// the Outstanding figure. The org/user come from the token; any active member may read.
func (h *Handler) GetInvoiceTimeline(c *gin.Context) {
	res, err := h.svc.InvoiceTimeline(c.Request.Context(), kernel.GetAuthUserID(c), kernel.GetAuthOrgID(c))
	if err != nil {
		kernel.RespondError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"invoice_timeline": res})
}

// GetBanking handles GET /api/v1/overview/banking — the Banking card: the org's
// month-end total bank balance over the last 12 months, plus the current total
// balance and the live-account count. The org/user come from the token; any active
// member may read.
func (h *Handler) GetBanking(c *gin.Context) {
	res, err := h.svc.Banking(c.Request.Context(), kernel.GetAuthUserID(c), kernel.GetAuthOrgID(c))
	if err != nil {
		kernel.RespondError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"banking": res})
}
