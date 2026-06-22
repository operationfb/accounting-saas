package expenses

// handler.go
// =============================================================================
// HTTP handlers for the expenses domain + self-registered routes. Extracted from
// server.go; receivers are now (h *Handler) delegating to h.svc (the Service).
// =============================================================================

import (
	"encoding/csv"
	"fmt"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	kernel "github.com/operationfb/accounting-saas/internal/kernel"
	"github.com/operationfb/accounting-saas/token"
)

// Handler is the HTTP boundary for the expenses domain.
type Handler struct{ svc *Service }

// NewHandler builds the expenses HTTP handler.
func NewHandler(svc *Service) *Handler { return &Handler{svc: svc} }

// RegisterRoutes mounts the expense + reference-data routes under /api/v1, each
// behind auth. The attachment sub-resource (/expenses/:id/attachments, /capture)
// is registered separately by the attachments package (same :id wildcard).
func (h *Handler) RegisterRoutes(r *gin.Engine, tokenMaker token.Maker) {
	expenses := r.Group("/api/v1/expenses")
	expenses.Use(kernel.AuthMiddleware(tokenMaker))
	{
		expenses.GET("", h.handleListExpenses)
		expenses.POST("", h.handleCreateExpense)
		expenses.GET("/inbox", h.handleListInbox)
		expenses.POST("/export", h.handleExportExpenses)
		expenses.GET("/:id", h.handleGetExpense)
		expenses.PUT("/:id", h.handleUpdateExpense)
		expenses.DELETE("/:id", h.handleDeleteExpense)
		expenses.POST("/:id/status", h.handleChangeExpenseStatus)
	}

	categories := r.Group("/api/v1/expense-categories")
	categories.Use(kernel.AuthMiddleware(tokenMaker))
	{
		categories.GET("", h.handleListExpenseCategories)
	}

	vatRates := r.Group("/api/v1/vat-rates")
	vatRates.Use(kernel.AuthMiddleware(tokenMaker))
	{
		vatRates.GET("", h.handleListVATRates)
	}
}

// handleCreateExpense handles POST /api/v1/expenses
//
// Flow:
//  1. Bind JSON body into CreateExpenseRequest (validates required fields)
//  2. Extract organisation_id from context (set by auth middleware — stubbed here)
//  3. Call expenseService.CreateExpense
//  4. Return 201 Created with the new expense as JSON
func (h *Handler) handleCreateExpense(c *gin.Context) {
	// Step 1: bind + validate the request body. kernel.BindJSON deserialises the JSON,
	// runs the `binding:` tag validations, and on failure writes the standard 400
	// error envelope and returns false.
	var req CreateExpenseRequest
	if !kernel.BindJSON(c, &req) {
		return
	}

	// Step 2: Identify the caller from the authenticated token (set by
	// authMiddleware). The claimant and organisation come from here — never from
	// the request body — so a user can only create expenses for themselves.
	userID := kernel.GetAuthUserID(c)
	orgID := kernel.GetAuthOrgID(c)

	// Step 3: Call the service layer.
	// The service handles authorisation, business logic, unit conversion, and
	// database writes.
	expense, err := h.svc.CreateExpense(c.Request.Context(), userID, orgID, req)
	if err != nil {
		kernel.RespondError(c, err)
		return
	}

	// Step 4: Return 201 Created.
	// gin.H is just map[string]any — a shorthand for building JSON objects.
	c.JSON(http.StatusCreated, gin.H{"expense": expense})
}

// handleGetExpense handles GET /api/v1/expenses/:id
func (h *Handler) handleGetExpense(c *gin.Context) {
	// c.Param("id") extracts the :id segment from the URL path.
	// e.g. GET /api/v1/expenses/abc-123 → id = "abc-123"
	id := c.Param("id")

	userID := kernel.GetAuthUserID(c)
	orgID := kernel.GetAuthOrgID(c)

	expense, err := h.svc.GetExpenseDetail(c.Request.Context(), userID, orgID, id)

	if err != nil {
		kernel.RespondError(c, err)
		return
	}

	c.JSON(http.StatusOK, gin.H{"expense": expense})
}

// handleUpdateExpense handles PUT /api/v1/expenses/:id
//
// Full update of an expense's editable fields. The service enforces that the
// caller owns the expense (or is an owner/admin of the org) and that the expense
// is still editable (DRAFT or REJECTED).
func (h *Handler) handleUpdateExpense(c *gin.Context) {
	id := c.Param("id")

	var req UpdateExpenseRequest
	if !kernel.BindJSON(c, &req) {
		return
	}

	userID := kernel.GetAuthUserID(c)
	orgID := kernel.GetAuthOrgID(c)

	expense, err := h.svc.UpdateExpense(c.Request.Context(), userID, orgID, id, req)
	if err != nil {
		kernel.RespondError(c, err)
		return
	}

	c.JSON(http.StatusOK, gin.H{"expense": expense})
}

// handleChangeExpenseStatus handles POST /api/v1/expenses/:id/status
//
// Drives one approval-workflow transition (submit/approve/reject/reopen). The
// service enforces the state machine (legal from→to, 409 on a bad move) and
// authorisation (claimant-or-admin for submit/reopen, admin-only for
// approve/reject). On success it returns the updated expense, like update.
func (h *Handler) handleChangeExpenseStatus(c *gin.Context) {
	id := c.Param("id")

	var req ChangeExpenseStatusRequest
	if !kernel.BindJSON(c, &req) {
		return
	}

	userID := kernel.GetAuthUserID(c)
	orgID := kernel.GetAuthOrgID(c)

	expense, err := h.svc.ChangeExpenseStatus(c.Request.Context(), userID, orgID, id, req.Action, req.RejectionNote)
	if err != nil {
		kernel.RespondError(c, err)
		return
	}

	c.JSON(http.StatusOK, gin.H{"expense": expense})
}

// handleDeleteExpense handles DELETE /api/v1/expenses/:id
//
// Soft-deletes an expense (e.g. an abandoned Smart Upload draft). The service
// enforces that the caller owns the expense (or is an owner/admin of the org) and
// that it is still editable (DRAFT or REJECTED). Returns 204 No Content.
func (h *Handler) handleDeleteExpense(c *gin.Context) {
	id := c.Param("id")
	userID := kernel.GetAuthUserID(c)
	orgID := kernel.GetAuthOrgID(c)

	if err := h.svc.DeleteExpense(c.Request.Context(), userID, orgID, id); err != nil {
		kernel.RespondError(c, err)
		return
	}

	c.Status(http.StatusNoContent)
}

// handleListExpenses handles GET /api/v1/expenses
//
// Returns the expenses the caller is allowed to see: owners/admins get every
// expense in their organisation; everyone else gets only their own.
func (h *Handler) handleListExpenses(c *gin.Context) {
	userID := kernel.GetAuthUserID(c)
	orgID := kernel.GetAuthOrgID(c)

	list, err := h.svc.ListExpenses(c.Request.Context(), userID, orgID)
	if err != nil {
		kernel.RespondError(c, err)
		return
	}

	c.JSON(http.StatusOK, gin.H{"expenses": list})
}

// handleExportExpenses handles POST /api/v1/expenses/export
//
// Streams expenses as a CSV download. The (optional) JSON body carries the ids to
// export so the SPA can export exactly the rows it filtered for display:
//
//	{"ids": ["<uuid>", ...]}  → export exactly those (empty list → header only)
//	no body / no "ids"        → export everything the caller may see
//
// Either way the backend enforces the visibility rule (owners/admins: the whole
// org; everyone else: only their own) and the column set matches the import
// template at web/public/expense_import_template.csv, so an export round-trips.
func (h *Handler) handleExportExpenses(c *gin.Context) {
	userID := kernel.GetAuthUserID(c)
	orgID := kernel.GetAuthOrgID(c)

	// Optional body. A bare POST (no body) means "everything I may see"; a body
	// with "ids" (even an empty array) means "exactly these". *[]string lets us
	// tell an absent "ids" (nil → all) from an empty one ([] → none).
	var req struct {
		Ids *[]string `json:"ids"`
	}
	if c.Request.ContentLength != 0 {
		if !kernel.BindJSON(c, &req) {
			return
		}
	}

	var ids []uuid.UUID
	if req.Ids != nil {
		ids = make([]uuid.UUID, 0, len(*req.Ids))
		for _, raw := range *req.Ids {
			id, err := uuid.Parse(raw)
			if err != nil {
				c.JSON(http.StatusUnprocessableEntity, gin.H{"error": gin.H{
					"code":    "validation_error",
					"message": fmt.Sprintf("invalid expense id %q", raw),
				}})
				return
			}
			ids = append(ids, id)
		}
	}

	rows, err := h.svc.ExportExpenses(c.Request.Context(), userID, orgID, ids)
	if err != nil {
		kernel.RespondError(c, err)
		return
	}

	// Past this point we commit to a 200: set the download headers, then stream
	// the CSV straight to the response writer. Write errors here only happen if
	// the client disconnects mid-stream — there's nothing useful to return.
	filename := fmt.Sprintf("expenses-%s.csv", time.Now().Format("2006-01-02"))
	c.Header("Content-Type", "text/csv; charset=utf-8")
	c.Header("Content-Disposition", fmt.Sprintf("attachment; filename=%q", filename))

	w := csv.NewWriter(c.Writer)
	_ = w.Write(ExpenseExportHeader) // header row first
	for _, r := range rows {
		_ = w.Write(r.record())
	}
	w.Flush()
}

// handleListInbox handles GET /api/v1/expenses/inbox
//
// Returns the Smart Upload captures awaiting review (needs_review). Owners/admins
// see the whole organisation's inbox; everyone else sees only their own captures.
func (h *Handler) handleListInbox(c *gin.Context) {
	list, err := h.svc.ListInbox(c.Request.Context(), kernel.GetAuthUserID(c), kernel.GetAuthOrgID(c))
	if err != nil {
		kernel.RespondError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"expenses": list})
}

// handleListExpenseCategories handles GET /api/v1/expense-categories
//
// Returns the active expense categories for the caller's organisation (taken
// from the authenticated token), used to populate the category picker.
func (h *Handler) handleListExpenseCategories(c *gin.Context) {
	userID := kernel.GetAuthUserID(c)
	orgID := kernel.GetAuthOrgID(c)

	list, err := h.svc.ListExpenseCategories(c.Request.Context(), userID, orgID)
	if err != nil {
		kernel.RespondError(c, err)
		return
	}

	c.JSON(http.StatusOK, gin.H{"expense_categories": list})
}

// handleListVATRates handles GET /api/v1/vat-rates
//
// Returns the VAT rates valid today for the caller's organisation's country
// (resolved from the authenticated token, never a request param), used to
// populate the VAT rate picker.
func (h *Handler) handleListVATRates(c *gin.Context) {
	userID := kernel.GetAuthUserID(c)
	orgID := kernel.GetAuthOrgID(c)

	list, err := h.svc.ListVATRates(c.Request.Context(), userID, orgID)
	if err != nil {
		kernel.RespondError(c, err)
		return
	}

	c.JSON(http.StatusOK, gin.H{"vat_rates": list})
}
