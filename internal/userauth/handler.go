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

// RegisterRoutes mounts GET/PUT /api/v1/profile behind bearer-token auth.
func (h *Handler) RegisterRoutes(r *gin.Engine, tokenMaker token.Maker) {
	g := r.Group("/api/v1/profile")
	g.Use(kernel.AuthMiddleware(tokenMaker))
	{
		g.GET("", h.GetProfile)
		g.PUT("", h.UpdateProfile)
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
