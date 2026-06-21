package currencies

// handler.go
// =============================================================================
// The HTTP boundary for the currencies reference data. Like internal/integrations,
// this Handler registers its OWN route (RegisterRoutes) on the shared Gin engine
// from main — the root Server struct is never touched.
//
// One read-only endpoint: GET /api/v1/currencies. It sits behind the same bearer-
// token auth as the sibling reference endpoints (/expense-categories, /vat-rates).
// The data is global, so the handler does NOT scope by organisation.
// =============================================================================

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/operationfb/accounting-saas/internal/kernel"
	"github.com/operationfb/accounting-saas/token"
)

// Handler is the HTTP boundary for currencies.
type Handler struct {
	svc *Service
}

// NewHandler builds the Handler.
func NewHandler(svc *Service) *Handler {
	return &Handler{svc: svc}
}

// RegisterRoutes mounts the currencies routes on the shared engine, behind
// bearer-token auth (the SPA is authenticated anyway). Called from main on
// server.Router(), the per-domain registration pattern.
func (h *Handler) RegisterRoutes(r *gin.Engine, tokenMaker token.Maker) {
	g := r.Group("/api/v1/currencies")
	g.Use(kernel.AuthMiddleware(tokenMaker))
	{
		g.GET("", h.ListCurrencies)
	}
}

// ListCurrencies handles GET /api/v1/currencies — the full ISO 4217 list.
func (h *Handler) ListCurrencies(c *gin.Context) {
	list, err := h.svc.ListCurrencies(c.Request.Context())
	if err != nil {
		kernel.RespondError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"currencies": list})
}
