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
	"encoding/json"
	"mime/multipart"
	"net/http"
	"strings"

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
		g.POST("/:id/transactions/import/preview", h.ImportPreview)
		g.POST("/:id/transactions/import", h.ImportTransactions)
		g.PUT("/:id/transactions/:txnId", h.UpdateTransaction)
		g.DELETE("/:id/transactions/:txnId", h.DeleteTransaction)
		// Explain / reconcile: a transaction's explanations (read any member; write owner/admin).
		g.GET("/:id/transactions/:txnId/explanations", h.ListExplanations)
		g.POST("/:id/transactions/:txnId/explanations", h.CreateExplanation)
		g.PUT("/:id/transactions/:txnId/explanations/:explId", h.UpdateExplanation)
		g.DELETE("/:id/transactions/:txnId/explanations/:explId", h.DeleteExplanation)
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

// ImportPreview handles POST /api/v1/bank-accounts/:id/transactions/import/preview — the
// DETECT step: upload a CSV/OFX (field "file") and get back the proposed column mapping +
// a sample of how rows would be read, WITHOUT importing anything. An optional "mapping"
// field (JSON) re-previews with the user's edits for live feedback in the confirm screen.
func (h *Handler) ImportPreview(c *gin.Context) {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxStatementUploadBytes)
	mapping, err := statementMappingFromForm(c)
	if err != nil {
		kernel.RespondError(c, err)
		return
	}
	f, err := openUploadedStatement(c)
	if err != nil {
		kernel.RespondError(c, err)
		return
	}
	defer f.Close()
	resp, err := h.svc.PreviewStatement(c.Request.Context(), kernel.GetAuthUserID(c), kernel.GetAuthOrgID(c), c.Param("id"), f, mapping)
	if err != nil {
		kernel.RespondError(c, err)
		return
	}
	c.JSON(http.StatusOK, resp)
}

// ImportTransactions handles POST /api/v1/bank-accounts/:id/transactions/import — the
// COMMIT step: a multipart CSV/OFX upload (field "file") plus an optional "mapping" field
// (JSON, the mapping the user confirmed in the detect step). With no mapping the service
// auto-detects, so our own template and the legacy endpoint contract keep working. Mirrors
// the attachment upload: cap the body before parsing, then stream the file to the service.
func (h *Handler) ImportTransactions(c *gin.Context) {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxStatementUploadBytes)
	mapping, err := statementMappingFromForm(c)
	if err != nil {
		kernel.RespondError(c, err)
		return
	}
	f, err := openUploadedStatement(c)
	if err != nil {
		kernel.RespondError(c, err)
		return
	}
	defer f.Close()
	resp, err := h.svc.ImportStatement(c.Request.Context(), kernel.GetAuthUserID(c), kernel.GetAuthOrgID(c), c.Param("id"), f, mapping)
	if err != nil {
		kernel.RespondError(c, err)
		return
	}
	c.JSON(http.StatusOK, resp)
}

// openUploadedStatement opens the multipart "file" part (shared by preview + commit). The
// caller must Close the returned file. A missing part (or an over-cap upload that tripped
// MaxBytesReader) becomes a 422.
func openUploadedStatement(c *gin.Context) (multipart.File, error) {
	fileHeader, err := c.FormFile("file")
	if err != nil {
		return nil, kernel.ErrValidation("a multipart 'file' field is required (or the upload was too large)", err)
	}
	f, err := fileHeader.Open()
	if err != nil {
		return nil, kernel.ErrValidation("could not read the uploaded file", err)
	}
	return f, nil
}

// statementMappingFromForm reads the optional "mapping" form field (JSON) into a
// ColumnMapping. Absent/blank → nil (the service auto-detects). Malformed JSON → 422.
func statementMappingFromForm(c *gin.Context) (*ColumnMapping, error) {
	raw := strings.TrimSpace(c.PostForm("mapping"))
	if raw == "" {
		return nil, nil
	}
	var m ColumnMapping
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		return nil, kernel.ErrValidation("the 'mapping' field is not valid JSON", err)
	}
	return &m, nil
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

// ListExplanations handles GET /…/:txnId/explanations — a line's explanations + its
// reconcile state (status + remaining). Any active member.
func (h *Handler) ListExplanations(c *gin.Context) {
	resp, err := h.svc.ListExplanations(c.Request.Context(), kernel.GetAuthUserID(c), kernel.GetAuthOrgID(c), c.Param("id"), c.Param("txnId"))
	if err != nil {
		kernel.RespondError(c, err)
		return
	}
	c.JSON(http.StatusOK, resp)
}

// CreateExplanation handles POST /…/:txnId/explanations — explain (part of) a line.
func (h *Handler) CreateExplanation(c *gin.Context) {
	var req CreateExplanationRequest
	if !kernel.BindJSON(c, &req) {
		return
	}
	resp, err := h.svc.CreateExplanation(c.Request.Context(), kernel.GetAuthUserID(c), kernel.GetAuthOrgID(c), c.Param("id"), c.Param("txnId"), req)
	if err != nil {
		kernel.RespondError(c, err)
		return
	}
	c.JSON(http.StatusCreated, resp)
}

// UpdateExplanation handles PUT /…/:txnId/explanations/:explId — edit one explanation.
func (h *Handler) UpdateExplanation(c *gin.Context) {
	var req CreateExplanationRequest
	if !kernel.BindJSON(c, &req) {
		return
	}
	resp, err := h.svc.UpdateExplanation(c.Request.Context(), kernel.GetAuthUserID(c), kernel.GetAuthOrgID(c), c.Param("id"), c.Param("txnId"), c.Param("explId"), req)
	if err != nil {
		kernel.RespondError(c, err)
		return
	}
	c.JSON(http.StatusOK, resp)
}

// DeleteExplanation handles DELETE /…/:txnId/explanations/:explId — un-explain a portion.
func (h *Handler) DeleteExplanation(c *gin.Context) {
	resp, err := h.svc.DeleteExplanation(c.Request.Context(), kernel.GetAuthUserID(c), kernel.GetAuthOrgID(c), c.Param("id"), c.Param("txnId"), c.Param("explId"))
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
