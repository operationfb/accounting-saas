package projects

// handler.go
// =============================================================================
// The HTTP boundary for the projects CRUD endpoints. Like the sibling domain
// packages (contacts, currencies, banking, integrations), this Handler registers
// its OWN routes (RegisterRoutes) on the shared Gin engine from main — the root
// Server struct is never touched. All routes sit behind bearer-token auth; the
// caller's identity (user + organisation) comes from the token, never the body.
// =============================================================================

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/operationfb/accounting-saas/internal/kernel"
	"github.com/operationfb/accounting-saas/token"
)

// Handler is the HTTP boundary for the projects endpoints.
type Handler struct {
	svc *Service
}

// NewHandler builds the Handler.
func NewHandler(svc *Service) *Handler {
	return &Handler{svc: svc}
}

// RegisterRoutes mounts the projects CRUD routes behind bearer-token auth.
func (h *Handler) RegisterRoutes(r *gin.Engine, tokenMaker token.Maker) {
	g := r.Group("/api/v1/projects")
	g.Use(kernel.AuthMiddleware(tokenMaker))
	{
		// GET    /api/v1/projects      → list the org's projects
		// POST   /api/v1/projects      → create a project
		// GET    /api/v1/projects/:id  → fetch one project by UUID
		// PUT    /api/v1/projects/:id  → full update
		// DELETE /api/v1/projects/:id  → hard delete
		g.GET("", h.ListProjects)
		g.POST("", h.CreateProject)
		g.GET("/:id", h.GetProject)
		g.PUT("/:id", h.UpdateProject)
		g.DELETE("/:id", h.DeleteProject)
	}
}

// ListProjects handles GET /api/v1/projects — every project in the caller's
// organisation.
func (h *Handler) ListProjects(c *gin.Context) {
	list, err := h.svc.ListProjects(c.Request.Context(), kernel.GetAuthUserID(c), kernel.GetAuthOrgID(c))
	if err != nil {
		kernel.RespondError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"projects": list})
}

// CreateProject handles POST /api/v1/projects — create one project. Returns 201.
func (h *Handler) CreateProject(c *gin.Context) {
	var req CreateProjectRequest
	if !kernel.BindJSON(c, &req) {
		return
	}
	project, err := h.svc.CreateProject(c.Request.Context(), kernel.GetAuthUserID(c), kernel.GetAuthOrgID(c), req)
	if err != nil {
		kernel.RespondError(c, err)
		return
	}
	c.JSON(http.StatusCreated, gin.H{"project": project})
}

// GetProject handles GET /api/v1/projects/:id.
func (h *Handler) GetProject(c *gin.Context) {
	project, err := h.svc.GetProject(c.Request.Context(), kernel.GetAuthUserID(c), kernel.GetAuthOrgID(c), c.Param("id"))
	if err != nil {
		kernel.RespondError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"project": project})
}

// UpdateProject handles PUT /api/v1/projects/:id — full update.
func (h *Handler) UpdateProject(c *gin.Context) {
	var req UpdateProjectRequest
	if !kernel.BindJSON(c, &req) {
		return
	}
	project, err := h.svc.UpdateProject(c.Request.Context(), kernel.GetAuthUserID(c), kernel.GetAuthOrgID(c), c.Param("id"), req)
	if err != nil {
		kernel.RespondError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"project": project})
}

// DeleteProject handles DELETE /api/v1/projects/:id — hard delete. Returns 204.
func (h *Handler) DeleteProject(c *gin.Context) {
	if err := h.svc.DeleteProject(c.Request.Context(), kernel.GetAuthUserID(c), kernel.GetAuthOrgID(c), c.Param("id")); err != nil {
		kernel.RespondError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}
