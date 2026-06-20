package integrations

// handler.go
// =============================================================================
// The user-facing HTTP boundary: settings (status/disconnect), OAuth connect, and
// the manual re-push / push-status. The Handler registers its OWN routes
// (RegisterRoutes here, RegisterInternalRoutes in internal_handler.go), so adding
// a provider is a new Handler + a RegisterRoutes call in main — the god Server is
// never touched.
//
// All paths are parameterised by the provider slug (Service.Provider()), so for
// "freeagent" they are byte-identical to the historical hardcoded paths.
// =============================================================================

import (
	"context"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/operationfb/accounting-saas/internal/kernel"
	"github.com/operationfb/accounting-saas/token"
)

// ExpenseRepublisher re-emits the expense.approved event for the manual re-push.
// A narrow interface (satisfied by *main.ExpenseService) so this package needn't
// depend on the whole expense domain.
type ExpenseRepublisher interface {
	RepublishApprovedExpense(ctx context.Context, authUserID, authOrgID uuid.UUID, id string) error
}

// Handler is the HTTP boundary for one integration provider.
type Handler struct {
	svc         *Service
	republisher ExpenseRepublisher
}

// NewHandler builds the Handler. republisher is injected (the re-push action).
func NewHandler(svc *Service, republisher ExpenseRepublisher) *Handler {
	return &Handler{svc: svc, republisher: republisher}
}

// RegisterRoutes mounts the user-facing routes on the shared engine.
func (h *Handler) RegisterRoutes(r *gin.Engine, tokenMaker token.Maker) {
	p := h.svc.Provider()

	// Settings + manual re-push/status (owner/admin; org from the token).
	settings := r.Group("/api/v1/integrations/" + p)
	settings.Use(kernel.AuthMiddleware(tokenMaker))
	{
		settings.GET("", h.GetStatus)
		settings.DELETE("", h.Disconnect)
		settings.POST("/expenses/:id/push", h.RepushExpense)
		settings.GET("/expenses/:id/push", h.GetPushStatus)
	}

	// OAuth dance: /connect is authed (returns the authorize URL as JSON);
	// /callback is PUBLIC (a top-level browser redirect from the provider).
	oauth := r.Group("/api/v1/" + p)
	{
		oauth.GET("/connect", kernel.AuthMiddleware(tokenMaker), h.Connect)
		oauth.GET("/callback", h.Callback)
	}
}

// =============================================================================
// HANDLERS
// =============================================================================

func (h *Handler) GetStatus(c *gin.Context) {
	status, err := h.svc.GetStatus(c.Request.Context(), kernel.GetAuthUserID(c), kernel.GetAuthOrgID(c))
	if err != nil {
		kernel.RespondError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"integration": status})
}

func (h *Handler) Disconnect(c *gin.Context) {
	if err := h.svc.Disconnect(c.Request.Context(), kernel.GetAuthUserID(c), kernel.GetAuthOrgID(c)); err != nil {
		kernel.RespondError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

// RepushExpense re-publishes the expense.approved event (owner/admin, APPROVED
// expenses only); the workflow's already_pushed guard makes it idempotent.
func (h *Handler) RepushExpense(c *gin.Context) {
	if err := h.republisher.RepublishApprovedExpense(c.Request.Context(), kernel.GetAuthUserID(c), kernel.GetAuthOrgID(c), c.Param("id")); err != nil {
		kernel.RespondError(c, err)
		return
	}
	c.Status(http.StatusAccepted)
}

func (h *Handler) GetPushStatus(c *gin.Context) {
	status, err := h.svc.GetExpensePushStatus(c.Request.Context(), kernel.GetAuthUserID(c), kernel.GetAuthOrgID(c), c.Param("id"))
	if err != nil {
		kernel.RespondError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"push": status})
}

// Connect returns the authorize URL as JSON — the SPA navigates to it. Not a 302
// (a redirect can't carry the SPA's bearer token).
func (h *Handler) Connect(c *gin.Context) {
	authorizeURL, err := h.svc.BuildConnectURL(c.Request.Context(), kernel.GetAuthUserID(c), kernel.GetAuthOrgID(c))
	if err != nil {
		kernel.RespondError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"authorize_url": authorizeURL})
}

// Callback is PUBLIC — the provider redirects the browser here with no bearer
// token; the org is recovered from the signed state. It 302s back to the SPA.
func (h *Handler) Callback(c *gin.Context) {
	redirectURL, err := h.svc.HandleCallback(c.Request.Context(), c.Query("code"), c.Query("state"))
	if err != nil {
		// The user is still redirected back (the URL encodes the error); this only
		// records the internal cause for ops.
		kernel.LogInternalError(c, err)
	}
	c.Redirect(http.StatusFound, redirectURL)
}
