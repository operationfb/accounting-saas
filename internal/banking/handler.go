package banking

// handler.go
// =============================================================================
// The HTTP boundary for bank accounts. Like internal/currencies and
// internal/integrations, this Handler registers its OWN routes (RegisterRoutes)
// on the shared Gin engine from main — the root Server struct is never touched.
//
// Five endpoints under /api/v1/bank-accounts, all behind bearer-token auth:
//   GET    ""      list the org's accounts (with derived balances)
//   POST   ""      create an account            (owner/admin)
//   GET    /:id    fetch one account
//   PUT    /:id    full update                  (owner/admin)
//   DELETE /:id    soft-delete                  (owner/admin)
// =============================================================================

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/operationfb/accounting-saas/internal/kernel"
	"github.com/operationfb/accounting-saas/token"
)

// Handler is the HTTP boundary for bank accounts.
type Handler struct {
	svc *Service
}

// NewHandler builds the Handler.
func NewHandler(svc *Service) *Handler {
	return &Handler{svc: svc}
}

// RegisterRoutes mounts the bank-account routes on the shared engine, behind
// bearer-token auth. Called from main on server.Router(), the per-domain pattern.
func (h *Handler) RegisterRoutes(r *gin.Engine, tokenMaker token.Maker) {
	g := r.Group("/api/v1/bank-accounts")
	g.Use(kernel.AuthMiddleware(tokenMaker))
	{
		g.GET("", h.List)
		g.POST("", h.Create)
		g.GET("/:id", h.Get)
		g.PUT("/:id", h.Update)
		g.DELETE("/:id", h.Delete)
	}
}

// List handles GET /api/v1/bank-accounts.
func (h *Handler) List(c *gin.Context) {
	list, err := h.svc.ListBankAccounts(c.Request.Context(), kernel.GetAuthUserID(c), kernel.GetAuthOrgID(c))
	if err != nil {
		kernel.RespondError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"bank_accounts": list})
}

// Create handles POST /api/v1/bank-accounts.
func (h *Handler) Create(c *gin.Context) {
	var req CreateBankAccountRequest
	if !kernel.BindJSON(c, &req) {
		return
	}
	account, err := h.svc.CreateBankAccount(c.Request.Context(), kernel.GetAuthUserID(c), kernel.GetAuthOrgID(c), req)
	if err != nil {
		kernel.RespondError(c, err)
		return
	}
	c.JSON(http.StatusCreated, gin.H{"bank_account": account})
}

// Get handles GET /api/v1/bank-accounts/:id.
func (h *Handler) Get(c *gin.Context) {
	account, err := h.svc.GetBankAccount(c.Request.Context(), kernel.GetAuthUserID(c), kernel.GetAuthOrgID(c), c.Param("id"))
	if err != nil {
		kernel.RespondError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"bank_account": account})
}

// Update handles PUT /api/v1/bank-accounts/:id.
func (h *Handler) Update(c *gin.Context) {
	var req CreateBankAccountRequest
	if !kernel.BindJSON(c, &req) {
		return
	}
	account, err := h.svc.UpdateBankAccount(c.Request.Context(), kernel.GetAuthUserID(c), kernel.GetAuthOrgID(c), c.Param("id"), req)
	if err != nil {
		kernel.RespondError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"bank_account": account})
}

// Delete handles DELETE /api/v1/bank-accounts/:id.
func (h *Handler) Delete(c *gin.Context) {
	if err := h.svc.DeleteBankAccount(c.Request.Context(), kernel.GetAuthUserID(c), kernel.GetAuthOrgID(c), c.Param("id")); err != nil {
		kernel.RespondError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}
