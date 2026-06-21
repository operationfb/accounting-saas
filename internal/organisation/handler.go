package organisation

// handler.go
// =============================================================================
// The HTTP boundary for the Company Details endpoints. Like the sibling domain
// packages (currencies, banking, integrations), this Handler registers its OWN
// routes (RegisterRoutes) on the shared Gin engine from main — the root Server
// struct is never touched.
// =============================================================================

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/operationfb/accounting-saas/internal/kernel"
	"github.com/operationfb/accounting-saas/token"
)

// Handler is the HTTP boundary for the Company Details endpoints.
type Handler struct {
	svc *Service
}

// NewHandler builds the Handler.
func NewHandler(svc *Service) *Handler {
	return &Handler{svc: svc}
}

// RegisterRoutes mounts GET/PUT /api/v1/organisation behind bearer-token auth.
// There is always exactly one organisation in scope (the caller's, from the
// token), so this is a singleton resource — no id in the path.
func (h *Handler) RegisterRoutes(r *gin.Engine, tokenMaker token.Maker) {
	g := r.Group("/api/v1/organisation")
	g.Use(kernel.AuthMiddleware(tokenMaker))
	{
		g.GET("", h.GetOrganisation)
		g.PUT("", h.UpdateOrganisation)
	}
}

// GetOrganisation handles GET /api/v1/organisation — the caller's company
// details. The org is taken from the token; any active member may read (a
// non-member gets 403 from the service).
func (h *Handler) GetOrganisation(c *gin.Context) {
	org, err := h.svc.GetOrganisation(c.Request.Context(), kernel.GetAuthUserID(c), kernel.GetAuthOrgID(c))
	if err != nil {
		kernel.RespondError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"organisation": org})
}

// UpdateOrganisation handles PUT /api/v1/organisation — update the caller's
// company details. The org is taken from the token; the service restricts editing
// to owners/admins (a plain member gets 403).
func (h *Handler) UpdateOrganisation(c *gin.Context) {
	var req UpdateOrganisationRequest
	if !kernel.BindJSON(c, &req) {
		return
	}
	org, err := h.svc.UpdateOrganisation(c.Request.Context(), kernel.GetAuthUserID(c), kernel.GetAuthOrgID(c), req)
	if err != nil {
		kernel.RespondError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"organisation": org})
}
