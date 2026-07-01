package userauth

// handler.go
// =============================================================================
// The HTTP boundary for the "My Details" profile endpoints. Like the sibling
// domain packages, this Handler registers its OWN routes (RegisterRoutes) on the
// shared Gin engine from main — behind bearer-token auth (a caller can only ever
// read/edit their own profile, taken from the token). The login/password-reset
// routes are registered separately by AuthHandler (auth.go) and are PUBLIC.
// =============================================================================

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/operationfb/accounting-saas/internal/kernel"
	"github.com/operationfb/accounting-saas/token"
)

// Handler is the HTTP boundary for the profile endpoints.
type Handler struct {
	svc *Service
}

// NewHandler builds the profile Handler.
func NewHandler(svc *Service) *Handler {
	return &Handler{svc: svc}
}

// RegisterRoutes mounts the caller's own session resources behind bearer-token
// auth: GET/PUT /api/v1/profile ("My Details") and the /api/v1/me/organisations
// org switcher. Both groups are self-scoped via the token — a caller can only
// read/switch to their OWN memberships.
func (h *Handler) RegisterRoutes(r *gin.Engine, tokenMaker token.Maker) {
	g := r.Group("/api/v1/profile")
	g.Use(kernel.AuthMiddleware(tokenMaker))
	{
		g.GET("", h.GetProfile)
		g.PUT("", h.UpdateProfile)
	}

	// Organisation switcher. A multi-org user lists the orgs they belong to and
	// re-scopes their session to another one (which re-mints the token).
	me := r.Group("/api/v1/me")
	me.Use(kernel.AuthMiddleware(tokenMaker))
	{
		me.GET("/organisations", h.ListMyOrganisations)
		me.POST("/organisations/switch", h.SwitchOrganisation)
	}
}

// GetProfile handles GET /api/v1/profile — the caller's own "My Details". The
// user is taken from the token, so a caller can only ever read themselves.
func (h *Handler) GetProfile(c *gin.Context) {
	profile, err := h.svc.GetProfile(c.Request.Context(), kernel.GetAuthUserID(c))
	if err != nil {
		kernel.RespondError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"user": profile})
}

// UpdateProfile handles PUT /api/v1/profile — update the caller's first/last
// name. The user is taken from the token, so it always targets themselves.
func (h *Handler) UpdateProfile(c *gin.Context) {
	var req UpdateProfileRequest
	if !kernel.BindJSON(c, &req) {
		return
	}
	profile, err := h.svc.UpdateProfile(c.Request.Context(), kernel.GetAuthUserID(c), req)
	if err != nil {
		kernel.RespondError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"user": profile})
}

// switchOrganisationRequest is the JSON body for POST /me/organisations/switch.
type switchOrganisationRequest struct {
	OrganisationID string `json:"organisation_id" binding:"required,uuid"`
}

// ListMyOrganisations handles GET /api/v1/me/organisations — every organisation
// the caller actively belongs to (id, name, country_code, role), for the top-bar
// switcher. The user is taken from the token, so it only ever lists their own.
func (h *Handler) ListMyOrganisations(c *gin.Context) {
	orgs, err := h.svc.ListMyOrganisations(c.Request.Context(), kernel.GetAuthUserID(c))
	if err != nil {
		kernel.RespondError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"organisations": orgs})
}

// SwitchOrganisation handles POST /api/v1/me/organisations/switch — re-scope the
// caller's session to a different organisation they belong to. Returns a fresh
// access token (same shape as login) which the client stores in place of the
// old one. The user is taken from the token; the target org is validated as one
// of the caller's active memberships in the service (403 otherwise).
func (h *Handler) SwitchOrganisation(c *gin.Context) {
	var req switchOrganisationRequest
	if !kernel.BindJSON(c, &req) {
		return
	}
	// `binding:"uuid"` already guarantees this parses; the error is defensive.
	orgID, err := uuid.Parse(req.OrganisationID)
	if err != nil {
		kernel.RespondError(c, kernel.ErrBadRequest("organisation_id must be a valid UUID", err))
		return
	}
	resp, err := h.svc.SwitchOrganisation(c.Request.Context(), kernel.GetAuthUserID(c), orgID)
	if err != nil {
		kernel.RespondError(c, err)
		return
	}
	c.JSON(http.StatusOK, resp)
}
