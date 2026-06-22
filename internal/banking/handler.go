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
		g.GET("/:id/transactions", h.ListTransactions)
		g.POST("/:id/transactions", h.CreateTransaction)
		g.POST("/:id/transactions/import", h.ImportTransactions)
		g.PUT("/:id/transactions/:txnId", h.UpdateTransaction)
		g.DELETE("/:id/transactions/:txnId", h.DeleteTransaction)
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

// ListTransactions handles GET /api/v1/bank-accounts/:id/transactions — the
// read-only statement: the account plus its lines (oldest first) with a running balance.
func (h *Handler) ListTransactions(c *gin.Context) {
	resp, err := h.svc.ListTransactions(c.Request.Context(), kernel.GetAuthUserID(c), kernel.GetAuthOrgID(c), c.Param("id"))
	if err != nil {
		kernel.RespondError(c, err)
		return
	}
	c.JSON(http.StatusOK, resp)
}

// CreateTransaction handles POST /api/v1/bank-accounts/:id/transactions — add a manual line.
func (h *Handler) CreateTransaction(c *gin.Context) {
	var req CreateBankTransactionRequest
	if !kernel.BindJSON(c, &req) {
		return
	}
	resp, err := h.svc.CreateTransaction(c.Request.Context(), kernel.GetAuthUserID(c), kernel.GetAuthOrgID(c), c.Param("id"), req)
	if err != nil {
		kernel.RespondError(c, err)
		return
	}
	c.JSON(http.StatusCreated, resp)
}

// ImportTransactions handles POST /api/v1/bank-accounts/:id/transactions/import — a
// multipart CSV statement upload (field "file"). Mirrors the attachment upload: cap
// the body before parsing, then stream the file to the import service.
func (h *Handler) ImportTransactions(c *gin.Context) {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxStatementUploadBytes)
	fileHeader, err := c.FormFile("file")
	if err != nil {
		kernel.RespondError(c, kernel.ErrValidation("a multipart 'file' field is required (or the upload was too large)", err))
		return
	}
	f, err := fileHeader.Open()
	if err != nil {
		kernel.RespondError(c, kernel.ErrValidation("could not read the uploaded file", err))
		return
	}
	defer f.Close()
	resp, err := h.svc.ImportStatement(c.Request.Context(), kernel.GetAuthUserID(c), kernel.GetAuthOrgID(c), c.Param("id"), f)
	if err != nil {
		kernel.RespondError(c, err)
		return
	}
	c.JSON(http.StatusOK, resp)
}

// UpdateTransaction handles PUT /api/v1/bank-accounts/:id/transactions/:txnId — edit a manual line.
func (h *Handler) UpdateTransaction(c *gin.Context) {
	var req CreateBankTransactionRequest
	if !kernel.BindJSON(c, &req) {
		return
	}
	resp, err := h.svc.UpdateTransaction(c.Request.Context(), kernel.GetAuthUserID(c), kernel.GetAuthOrgID(c), c.Param("id"), c.Param("txnId"), req)
	if err != nil {
		kernel.RespondError(c, err)
		return
	}
	c.JSON(http.StatusOK, resp)
}

// DeleteTransaction handles DELETE /api/v1/bank-accounts/:id/transactions/:txnId — remove a manual line.
func (h *Handler) DeleteTransaction(c *gin.Context) {
	resp, err := h.svc.DeleteTransaction(c.Request.Context(), kernel.GetAuthUserID(c), kernel.GetAuthOrgID(c), c.Param("id"), c.Param("txnId"))
	if err != nil {
		kernel.RespondError(c, err)
		return
	}
	c.JSON(http.StatusOK, resp)
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
