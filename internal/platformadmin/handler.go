package platformadmin

// handler.go
// =============================================================================
// The HTTP boundary for the platform-admin ("god view") endpoints. Like the
// sibling domain packages, this Handler self-registers its routes on the shared
// Gin engine from main — behind bearer-token auth. The SERVICE then enforces the
// superuser gate on every call (a normal authenticated caller gets 403), so the
// routes carry no extra middleware.
// =============================================================================

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/operationfb/accounting-saas/internal/kernel"
	"github.com/operationfb/accounting-saas/internal/organisation"
	"github.com/operationfb/accounting-saas/token"
)

// Handler is the HTTP boundary for the platform-admin endpoints.
type Handler struct {
	svc *Service
}

// NewHandler builds the Handler.
func NewHandler(svc *Service) *Handler {
	return &Handler{svc: svc}
}

// RegisterRoutes mounts the read-only /api/v1/admin routes behind bearer-token
// auth. The service restricts them to superusers (403 otherwise).
func (h *Handler) RegisterRoutes(r *gin.Engine, tokenMaker token.Maker) {
	g := r.Group("/api/v1/admin")
	g.Use(kernel.AuthMiddleware(tokenMaker))
	{
		g.GET("/organisations", h.ListOrganisations)
		g.POST("/organisations", h.CreateOrganisation)
		g.GET("/organisations/:id", h.GetOrganisation)
		// Company details for a chosen org — the one editable god-view surface.
		g.GET("/organisations/:id/company-details", h.GetOrganisationDetails)
		g.PUT("/organisations/:id/company-details", h.UpdateOrganisationDetails)
		// Member management for a chosen org (add existing / create new user).
		g.POST("/organisations/:id/members", h.AddOrganisationMember)
		g.POST("/organisations/:id/users", h.CreateOrganisationUser)
		g.GET("/users", h.ListUsers)
		g.GET("/users/:id", h.GetUser)
	}
}

// parseIDParam reads the :id path param as a UUID, writing a 400 and returning
// ok=false on a malformed value.
func parseIDParam(c *gin.Context) (uuid.UUID, bool) {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		kernel.RespondError(c, kernel.ErrBadRequest("id must be a valid UUID", err))
		return uuid.Nil, false
	}
	return id, true
}

// ListOrganisations handles GET /api/v1/admin/organisations — every org on the
// platform. Superuser only (enforced in the service).
func (h *Handler) ListOrganisations(c *gin.Context) {
	list, err := h.svc.ListOrganisations(c.Request.Context(), kernel.GetAuthUserID(c))
	if err != nil {
		kernel.RespondError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"organisations": list})
}

// CreateOrganisation handles POST /api/v1/admin/organisations — create a new org
// and provision its chart of accounts. Superuser only. Returns 201 with the new
// org.
func (h *Handler) CreateOrganisation(c *gin.Context) {
	var req CreateOrganisationRequest
	if !kernel.BindJSON(c, &req) {
		return
	}
	resp, err := h.svc.CreateOrganisation(c.Request.Context(), kernel.GetAuthUserID(c), req)
	if err != nil {
		kernel.RespondError(c, err)
		return
	}
	c.JSON(http.StatusCreated, gin.H{"organisation": resp})
}

// GetOrganisation handles GET /api/v1/admin/organisations/:id — one org + its
// members. Superuser only.
func (h *Handler) GetOrganisation(c *gin.Context) {
	orgID, ok := parseIDParam(c)
	if !ok {
		return
	}
	resp, err := h.svc.GetOrganisation(c.Request.Context(), kernel.GetAuthUserID(c), orgID)
	if err != nil {
		kernel.RespondError(c, err)
		return
	}
	c.JSON(http.StatusOK, resp)
}

// GetOrganisationDetails handles GET /api/v1/admin/organisations/:id/company-details
// — a chosen org's full company details. Superuser only.
func (h *Handler) GetOrganisationDetails(c *gin.Context) {
	orgID, ok := parseIDParam(c)
	if !ok {
		return
	}
	resp, err := h.svc.GetOrganisationDetails(c.Request.Context(), kernel.GetAuthUserID(c), orgID)
	if err != nil {
		kernel.RespondError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"organisation": resp})
}

// UpdateOrganisationDetails handles PUT /api/v1/admin/organisations/:id/company-details
// — edit a chosen org's company details. Superuser only. Reuses the organisation
// domain's request DTO, so country_code/native_currency stay immutable here too.
func (h *Handler) UpdateOrganisationDetails(c *gin.Context) {
	orgID, ok := parseIDParam(c)
	if !ok {
		return
	}
	var req organisation.UpdateOrganisationRequest
	if !kernel.BindJSON(c, &req) {
		return
	}
	resp, err := h.svc.UpdateOrganisationDetails(c.Request.Context(), kernel.GetAuthUserID(c), orgID, req)
	if err != nil {
		kernel.RespondError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"organisation": resp})
}

// AddOrganisationMember handles POST /api/v1/admin/organisations/:id/members —
// attach an existing user to the org. Superuser only. Returns the refreshed org
// detail.
func (h *Handler) AddOrganisationMember(c *gin.Context) {
	orgID, ok := parseIDParam(c)
	if !ok {
		return
	}
	var req AddOrganisationMemberRequest
	if !kernel.BindJSON(c, &req) {
		return
	}
	resp, err := h.svc.AddOrganisationMember(c.Request.Context(), kernel.GetAuthUserID(c), orgID, req)
	if err != nil {
		kernel.RespondError(c, err)
		return
	}
	// Return the refreshed detail unwrapped, matching GET /organisations/:id.
	c.JSON(http.StatusCreated, resp)
}

// CreateOrganisationUser handles POST /api/v1/admin/organisations/:id/users —
// create a new user and attach them to the org. Superuser only. Returns the
// refreshed org detail.
func (h *Handler) CreateOrganisationUser(c *gin.Context) {
	orgID, ok := parseIDParam(c)
	if !ok {
		return
	}
	var req CreateOrganisationUserRequest
	if !kernel.BindJSON(c, &req) {
		return
	}
	resp, err := h.svc.CreateOrganisationUser(c.Request.Context(), kernel.GetAuthUserID(c), orgID, req)
	if err != nil {
		kernel.RespondError(c, err)
		return
	}
	// Return the refreshed detail unwrapped, matching GET /organisations/:id.
	c.JSON(http.StatusCreated, resp)
}

// ListUsers handles GET /api/v1/admin/users — every user on the platform.
// Superuser only.
func (h *Handler) ListUsers(c *gin.Context) {
	list, err := h.svc.ListUsers(c.Request.Context(), kernel.GetAuthUserID(c))
	if err != nil {
		kernel.RespondError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"users": list})
}

// GetUser handles GET /api/v1/admin/users/:id — one user + the orgs they belong
// to. Superuser only.
func (h *Handler) GetUser(c *gin.Context) {
	targetID, ok := parseIDParam(c)
	if !ok {
		return
	}
	resp, err := h.svc.GetUser(c.Request.Context(), kernel.GetAuthUserID(c), targetID)
	if err != nil {
		kernel.RespondError(c, err)
		return
	}
	c.JSON(http.StatusOK, resp)
}
