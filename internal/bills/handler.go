package bills

// handler.go
// =============================================================================
// The HTTP boundary for the bills endpoints. Like the sibling domain packages
// (invoices, contacts, projects), this Handler registers its OWN routes
// (RegisterRoutes) on the shared Gin engine from main — the root Server struct is
// never touched. All routes sit behind bearer-token auth; the caller's identity
// (user + organisation) comes from the token, never the request body.
//
// There is no status route (bills have no lifecycle). The VAT-rate picker reuses
// the existing GET /api/v1/vat-rates (expenses handler); this handler adds only the
// spending-category picker GET /api/v1/bill-categories.
// =============================================================================

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/operationfb/accounting-saas/internal/kernel"
	"github.com/operationfb/accounting-saas/token"
)

// Handler is the HTTP boundary for the bills endpoints.
type Handler struct {
	svc *Service
}

// NewHandler builds the Handler.
func NewHandler(svc *Service) *Handler {
	return &Handler{svc: svc}
}

// RegisterRoutes mounts the bills routes behind bearer-token auth.
func (h *Handler) RegisterRoutes(r *gin.Engine, tokenMaker token.Maker) {
	g := r.Group("/api/v1/bills")
	g.Use(kernel.AuthMiddleware(tokenMaker))
	{
		// GET    /api/v1/bills      → list the org's bills
		// POST   /api/v1/bills      → create a bill
		// GET    /api/v1/bills/:id  → fetch one bill
		// PUT    /api/v1/bills/:id  → full update (while unpaid)
		// DELETE /api/v1/bills/:id  → soft-delete (while unpaid)
		g.GET("", h.ListBills)
		// Static route before the /:id wildcard so "outstanding" isn't captured as an id.
		g.GET("/outstanding", h.ListOutstandingBills)
		g.POST("", h.CreateBill)
		g.GET("/:id", h.GetBill)
		g.PUT("/:id", h.UpdateBill)
		g.DELETE("/:id", h.DeleteBill)
	}

	// The "Spending Category" picker — a sibling of /vat-rates, behind the same auth.
	r.GET("/api/v1/bill-categories", kernel.AuthMiddleware(tokenMaker), h.ListBillCategories)
}

// ListBills handles GET /api/v1/bills.
func (h *Handler) ListBills(c *gin.Context) {
	list, err := h.svc.ListBills(c.Request.Context(), kernel.GetAuthUserID(c), kernel.GetAuthOrgID(c))
	if err != nil {
		kernel.RespondError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"bills": list})
}

// ListOutstandingBills handles GET /api/v1/bills/outstanding — bills still owing
// money (the banking Bill Payment explanation picker).
func (h *Handler) ListOutstandingBills(c *gin.Context) {
	list, err := h.svc.ListOutstandingBills(c.Request.Context(), kernel.GetAuthUserID(c), kernel.GetAuthOrgID(c))
	if err != nil {
		kernel.RespondError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"bills": list})
}

// CreateBill handles POST /api/v1/bills. Returns 201 Created.
func (h *Handler) CreateBill(c *gin.Context) {
	var req CreateBillRequest
	if !kernel.BindJSON(c, &req) {
		return
	}
	bill, err := h.svc.CreateBill(c.Request.Context(), kernel.GetAuthUserID(c), kernel.GetAuthOrgID(c), req)
	if err != nil {
		kernel.RespondError(c, err)
		return
	}
	c.JSON(http.StatusCreated, gin.H{"bill": bill})
}

// GetBill handles GET /api/v1/bills/:id.
func (h *Handler) GetBill(c *gin.Context) {
	bill, err := h.svc.GetBill(c.Request.Context(), kernel.GetAuthUserID(c), kernel.GetAuthOrgID(c), c.Param("id"))
	if err != nil {
		kernel.RespondError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"bill": bill})
}

// UpdateBill handles PUT /api/v1/bills/:id — full update (while unpaid), allowed to
// the bill's creator or an owner/admin.
func (h *Handler) UpdateBill(c *gin.Context) {
	var req UpdateBillRequest
	if !kernel.BindJSON(c, &req) {
		return
	}
	bill, err := h.svc.UpdateBill(c.Request.Context(), kernel.GetAuthUserID(c), kernel.GetAuthOrgID(c), c.Param("id"), req)
	if err != nil {
		kernel.RespondError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"bill": bill})
}

// DeleteBill handles DELETE /api/v1/bills/:id — soft-delete (while unpaid). Returns
// 204 No Content.
func (h *Handler) DeleteBill(c *gin.Context) {
	if err := h.svc.DeleteBill(c.Request.Context(), kernel.GetAuthUserID(c), kernel.GetAuthOrgID(c), c.Param("id")); err != nil {
		kernel.RespondError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

// ListBillCategories handles GET /api/v1/bill-categories — the spending-category picker.
func (h *Handler) ListBillCategories(c *gin.Context) {
	list, err := h.svc.ListBillCategories(c.Request.Context(), kernel.GetAuthUserID(c), kernel.GetAuthOrgID(c))
	if err != nil {
		kernel.RespondError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"bill_categories": list})
}
