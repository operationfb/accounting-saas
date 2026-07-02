package tala

// handler.go
// =============================================================================
// The HTTP boundary for the Tala assistant. Like the sibling domains, the Handler
// self-registers its route (RegisterRoutes) on the shared Gin engine from main,
// behind bearer-token auth — so the org/user come from the token, never the body.
// =============================================================================

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/operationfb/accounting-saas/internal/kernel"
	"github.com/operationfb/accounting-saas/token"
)

// Handler is the HTTP boundary for the Tala endpoint.
type Handler struct {
	svc *Service
}

// NewHandler builds the Handler.
func NewHandler(svc *Service) *Handler { return &Handler{svc: svc} }

// RegisterRoutes mounts POST /api/v1/tala/chat behind bearer-token auth. Any
// active member may chat; each tool still runs its own service authorisation.
func (h *Handler) RegisterRoutes(r *gin.Engine, tokenMaker token.Maker) {
	g := r.Group("/api/v1/tala")
	g.Use(kernel.AuthMiddleware(tokenMaker))
	{
		g.POST("/chat", h.Chat)
	}
}

// Chat handles one conversation turn. The request carries the full history; the
// response carries Tala's reply plus any guarded-write proposals for the SPA to
// confirm.
func (h *Handler) Chat(c *gin.Context) {
	var req ChatRequest
	if !kernel.BindJSON(c, &req) {
		return
	}

	reply, proposals, toolsUsed, err := h.svc.RunTurn(
		c.Request.Context(),
		kernel.GetAuthUserID(c),
		kernel.GetAuthOrgID(c),
		req.Messages,
	)
	if err != nil {
		kernel.RespondError(c, err)
		return
	}

	// Normalise nils so the JSON always has arrays, not null (simpler for the SPA).
	if proposals == nil {
		proposals = []ProposedAction{}
	}
	if toolsUsed == nil {
		toolsUsed = []string{}
	}

	c.JSON(http.StatusOK, ChatResponse{
		Reply:           reply,
		ProposedActions: proposals,
		ToolCalls:       toolsUsed,
	})
}
