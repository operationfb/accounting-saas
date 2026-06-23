package invoices

// handler.go
// =============================================================================
// The HTTP boundary for the invoices endpoints. Like the sibling domain packages
// (contacts, projects, banking), this Handler registers its OWN routes
// (RegisterRoutes) on the shared Gin engine from main — the root Server struct is
// never touched. All routes sit behind bearer-token auth; the caller's identity
// (user + organisation) comes from the token, never the request body.
// =============================================================================

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/operationfb/accounting-saas/internal/kernel"
	"github.com/operationfb/accounting-saas/token"
)

// Handler is the HTTP boundary for the invoices endpoints.
type Handler struct {
	svc *Service
}

// NewHandler builds the Handler.
func NewHandler(svc *Service) *Handler {
	return &Handler{svc: svc}
}

// RegisterRoutes mounts the invoices routes behind bearer-token auth.
func (h *Handler) RegisterRoutes(r *gin.Engine, tokenMaker token.Maker) {
	g := r.Group("/api/v1/invoices")
	g.Use(kernel.AuthMiddleware(tokenMaker))
	{
		// GET    /api/v1/invoices          → list the org's invoices (no line items)
		// POST   /api/v1/invoices          → create a DRAFT invoice (+ line items)
		// GET    /api/v1/invoices/:id      → fetch one invoice + its line items
		// PUT    /api/v1/invoices/:id      → full update incl. lines (DRAFT only)
		// DELETE /api/v1/invoices/:id      → soft-delete (DRAFT only)
		// POST   /api/v1/invoices/:id/status → drive the status lifecycle
		g.GET("", h.ListInvoices)
		g.POST("", h.CreateInvoice)
		g.GET("/:id", h.GetInvoice)
		g.PUT("/:id", h.UpdateInvoice)
		g.DELETE("/:id", h.DeleteInvoice)
		g.POST("/:id/status", h.ChangeStatus)
	}
}

// ListInvoices handles GET /api/v1/invoices.
func (h *Handler) ListInvoices(c *gin.Context) {
	list, err := h.svc.ListInvoices(c.Request.Context(), kernel.GetAuthUserID(c), kernel.GetAuthOrgID(c))
	if err != nil {
		kernel.RespondError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"invoices": list})
}

// CreateInvoice handles POST /api/v1/invoices. Returns 201 Created.
func (h *Handler) CreateInvoice(c *gin.Context) {
	var req CreateInvoiceRequest
	if !kernel.BindJSON(c, &req) {
		return
	}
	inv, err := h.svc.CreateInvoice(c.Request.Context(), kernel.GetAuthUserID(c), kernel.GetAuthOrgID(c), req)
	if err != nil {
		kernel.RespondError(c, err)
		return
	}
	c.JSON(http.StatusCreated, gin.H{"invoice": inv})
}

// GetInvoice handles GET /api/v1/invoices/:id.
func (h *Handler) GetInvoice(c *gin.Context) {
	inv, err := h.svc.GetInvoice(c.Request.Context(), kernel.GetAuthUserID(c), kernel.GetAuthOrgID(c), c.Param("id"))
	if err != nil {
		kernel.RespondError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"invoice": inv})
}

// UpdateInvoice handles PUT /api/v1/invoices/:id — full update (DRAFT only),
// allowed to the invoice's creator or an owner/admin.
func (h *Handler) UpdateInvoice(c *gin.Context) {
	var req UpdateInvoiceRequest
	if !kernel.BindJSON(c, &req) {
		return
	}
	inv, err := h.svc.UpdateInvoice(c.Request.Context(), kernel.GetAuthUserID(c), kernel.GetAuthOrgID(c), c.Param("id"), req)
	if err != nil {
		kernel.RespondError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"invoice": inv})
}

// DeleteInvoice handles DELETE /api/v1/invoices/:id — soft-delete (DRAFT only).
// Returns 204 No Content.
func (h *Handler) DeleteInvoice(c *gin.Context) {
	if err := h.svc.DeleteInvoice(c.Request.Context(), kernel.GetAuthUserID(c), kernel.GetAuthOrgID(c), c.Param("id")); err != nil {
		kernel.RespondError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

// ChangeStatus handles POST /api/v1/invoices/:id/status — drive the status
// lifecycle via an {"action": …} discriminator.
func (h *Handler) ChangeStatus(c *gin.Context) {
	var req ChangeStatusRequest
	if !kernel.BindJSON(c, &req) {
		return
	}
	inv, err := h.svc.ChangeStatus(c.Request.Context(), kernel.GetAuthUserID(c), kernel.GetAuthOrgID(c), c.Param("id"), req.Action)
	if err != nil {
		kernel.RespondError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"invoice": inv})
}
