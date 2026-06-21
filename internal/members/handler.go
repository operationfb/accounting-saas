package members

// handler.go
// =============================================================================
// The HTTP boundary for the members endpoint. Like the sibling domain packages
// (contacts, projects, currencies, banking, integrations), this Handler registers
// its OWN route (RegisterRoutes) on the shared Gin engine from main — the root
// Server struct is never touched.
// =============================================================================

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/operationfb/accounting-saas/internal/kernel"
	"github.com/operationfb/accounting-saas/token"
)

// Handler is the HTTP boundary for the members endpoint.
type Handler struct {
	svc *Service
}

// NewHandler builds the Handler.
func NewHandler(svc *Service) *Handler {
	return &Handler{svc: svc}
}

// RegisterRoutes mounts GET /api/v1/members behind bearer-token auth (the service
// further restricts it to owners/admins).
func (h *Handler) RegisterRoutes(r *gin.Engine, tokenMaker token.Maker) {
	g := r.Group("/api/v1/members")
	g.Use(kernel.AuthMiddleware(tokenMaker))
	{
		g.GET("", h.ListMembers)
	}
}

// ListMembers handles GET /api/v1/members — every member of the caller's
// organisation. The org is taken from the token; the service restricts this to
// owners/admins (a plain member gets 403).
func (h *Handler) ListMembers(c *gin.Context) {
	list, err := h.svc.ListMembers(c.Request.Context(), kernel.GetAuthUserID(c), kernel.GetAuthOrgID(c))
	if err != nil {
		kernel.RespondError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"members": list})
}
