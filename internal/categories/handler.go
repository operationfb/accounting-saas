package categories

// handler.go
// =============================================================================
// HTTP boundary for the reconcile reference endpoints. Like the sibling domain
// packages (members, banking, …) it registers its OWN routes via RegisterRoutes
// on the shared Gin engine from main.
// =============================================================================

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/operationfb/accounting-saas/internal/kernel"
	"github.com/operationfb/accounting-saas/token"
)

// Handler is the HTTP boundary for the transaction-types + categories pickers.
type Handler struct {
	svc *Service
}

// NewHandler builds the Handler.
func NewHandler(svc *Service) *Handler {
	return &Handler{svc: svc}
}

// RegisterRoutes mounts the reference endpoints behind bearer-token auth (the
// service requires an active member). The explain UI reads these to drive the
// Type dropdown and its per-type category picker.
func (h *Handler) RegisterRoutes(r *gin.Engine, tokenMaker token.Maker) {
	g := r.Group("/api/v1/transaction-types")
	g.Use(kernel.AuthMiddleware(tokenMaker))
	{
		g.GET("", h.ListTransactionTypes)
		g.GET("/:code/categories", h.ListCategoriesForType)
	}
}

// ListTransactionTypes handles GET /api/v1/transaction-types — the 18 explanation
// types, each flagged supported/unsupported for v1.
func (h *Handler) ListTransactionTypes(c *gin.Context) {
	list, err := h.svc.ListTransactionTypes(c.Request.Context(), kernel.GetAuthUserID(c), kernel.GetAuthOrgID(c))
	if err != nil {
		kernel.RespondError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"transaction_types": list})
}

// ListCategoriesForType handles GET /api/v1/transaction-types/:code/categories —
// the CoA accounts that type offers for the caller's org (+ company_type).
func (h *Handler) ListCategoriesForType(c *gin.Context) {
	list, err := h.svc.ListCategoriesForType(c.Request.Context(), kernel.GetAuthUserID(c), kernel.GetAuthOrgID(c), c.Param("code"))
	if err != nil {
		kernel.RespondError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"categories": list})
}
