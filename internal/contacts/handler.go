package contacts

// handler.go
// =============================================================================
// The HTTP boundary for the contacts CRUD endpoints. Like the sibling domain
// packages (currencies, banking, integrations), this Handler registers its OWN
// routes (RegisterRoutes) on the shared Gin engine from main — the root Server
// struct is never touched. All routes sit behind bearer-token auth; the caller's
// identity (user + organisation) comes from the token, never the request body.
// =============================================================================

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/operationfb/accounting-saas/internal/kernel"
	"github.com/operationfb/accounting-saas/token"
)

// Handler is the HTTP boundary for the contacts endpoints.
type Handler struct {
	svc *Service
}

// NewHandler builds the Handler.
func NewHandler(svc *Service) *Handler {
	return &Handler{svc: svc}
}

// RegisterRoutes mounts the contacts CRUD routes behind bearer-token auth.
func (h *Handler) RegisterRoutes(r *gin.Engine, tokenMaker token.Maker) {
	g := r.Group("/api/v1/contacts")
	g.Use(kernel.AuthMiddleware(tokenMaker))
	{
		// GET    /api/v1/contacts      → list the org's contacts
		// POST   /api/v1/contacts      → create a contact
		// GET    /api/v1/contacts/:id  → fetch one contact by UUID
		// PUT    /api/v1/contacts/:id  → full update (creator or owner/admin)
		// DELETE /api/v1/contacts/:id  → soft-delete (creator or owner/admin)
		g.GET("", h.ListContacts)
		g.POST("", h.CreateContact)
		g.GET("/:id", h.GetContact)
		g.PUT("/:id", h.UpdateContact)
		g.DELETE("/:id", h.DeleteContact)
	}
}

// ListContacts handles GET /api/v1/contacts — every contact in the caller's
// organisation.
func (h *Handler) ListContacts(c *gin.Context) {
	list, err := h.svc.ListContacts(c.Request.Context(), kernel.GetAuthUserID(c), kernel.GetAuthOrgID(c))
	if err != nil {
		kernel.RespondError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"contacts": list})
}

// CreateContact handles POST /api/v1/contacts — create one contact for the
// caller's organisation. Returns 201 Created.
func (h *Handler) CreateContact(c *gin.Context) {
	var req CreateContactRequest
	if !kernel.BindJSON(c, &req) {
		return
	}
	contact, err := h.svc.CreateContact(c.Request.Context(), kernel.GetAuthUserID(c), kernel.GetAuthOrgID(c), req)
	if err != nil {
		kernel.RespondError(c, err)
		return
	}
	c.JSON(http.StatusCreated, gin.H{"contact": contact})
}

// GetContact handles GET /api/v1/contacts/:id.
func (h *Handler) GetContact(c *gin.Context) {
	contact, err := h.svc.GetContact(c.Request.Context(), kernel.GetAuthUserID(c), kernel.GetAuthOrgID(c), c.Param("id"))
	if err != nil {
		kernel.RespondError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"contact": contact})
}

// UpdateContact handles PUT /api/v1/contacts/:id — full update, allowed to the
// contact's creator or an owner/admin of the organisation.
func (h *Handler) UpdateContact(c *gin.Context) {
	var req UpdateContactRequest
	if !kernel.BindJSON(c, &req) {
		return
	}
	contact, err := h.svc.UpdateContact(c.Request.Context(), kernel.GetAuthUserID(c), kernel.GetAuthOrgID(c), c.Param("id"), req)
	if err != nil {
		kernel.RespondError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"contact": contact})
}

// DeleteContact handles DELETE /api/v1/contacts/:id — soft-delete, allowed to the
// contact's creator or an owner/admin. Returns 204 No Content.
func (h *Handler) DeleteContact(c *gin.Context) {
	if err := h.svc.DeleteContact(c.Request.Context(), kernel.GetAuthUserID(c), kernel.GetAuthOrgID(c), c.Param("id")); err != nil {
		kernel.RespondError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}
