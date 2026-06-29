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
	"github.com/google/uuid"

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

// RegisterRoutes mounts the members routes behind bearer-token auth (the service
// further restricts the admin ones to owners/admins).
func (h *Handler) RegisterRoutes(r *gin.Engine, tokenMaker token.Maker) {
	g := r.Group("/api/v1/members")
	g.Use(kernel.AuthMiddleware(tokenMaker))
	{
		g.GET("", h.ListMembers)
		g.POST("", h.CreateMember)
		g.GET("/:id", h.GetMember)
		g.PUT("/:id", h.UpdateMember)
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

// CreateMember handles POST /api/v1/members — an owner/admin creating a new user
// and attaching them to the organisation. Returns 201 with the created member's
// full detail. Owner/admin only (enforced in the service).
func (h *Handler) CreateMember(c *gin.Context) {
	var req CreateMemberRequest
	if !kernel.BindJSON(c, &req) {
		return
	}
	resp, err := h.svc.CreateMember(c.Request.Context(), kernel.GetAuthUserID(c), kernel.GetAuthOrgID(c), req)
	if err != nil {
		kernel.RespondError(c, err)
		return
	}
	c.JSON(http.StatusCreated, resp)
}

// GetMember handles GET /api/v1/members/:id — one member's full detail for the
// admin User Details screen. Owner/admin only (enforced in the service).
func (h *Handler) GetMember(c *gin.Context) {
	targetID, ok := parseMemberID(c)
	if !ok {
		return
	}
	resp, err := h.svc.GetMember(c.Request.Context(), kernel.GetAuthUserID(c), kernel.GetAuthOrgID(c), targetID)
	if err != nil {
		kernel.RespondError(c, err)
		return
	}
	c.JSON(http.StatusOK, resp)
}

// UpdateMember handles PUT /api/v1/members/:id — an owner/admin editing another
// user's details, role and status.
func (h *Handler) UpdateMember(c *gin.Context) {
	targetID, ok := parseMemberID(c)
	if !ok {
		return
	}
	var req UpdateMemberRequest
	if !kernel.BindJSON(c, &req) {
		return
	}
	resp, err := h.svc.UpdateMember(c.Request.Context(), kernel.GetAuthUserID(c), kernel.GetAuthOrgID(c), targetID, req)
	if err != nil {
		kernel.RespondError(c, err)
		return
	}
	c.JSON(http.StatusOK, resp)
}

// parseMemberID reads and validates the :id path param as a UUID, writing a 400
// and returning false on a malformed value.
func parseMemberID(c *gin.Context) (uuid.UUID, bool) {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		kernel.RespondError(c, kernel.ErrBadRequest("invalid user id", err))
		return uuid.Nil, false
	}
	return id, true
}
