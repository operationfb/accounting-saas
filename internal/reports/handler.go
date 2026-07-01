package reports

// handler.go
// =============================================================================
// The HTTP boundary for the financial reports. Like the sibling domain packages
// (overview, vat, banking), this Handler registers its OWN routes (RegisterRoutes)
// on the shared Gin engine from main — the root server is never touched. Reports
// are read-only and org-scoped (the org comes from the token); future reports
// (P&L, Balance Sheet) add sibling GET routes here.
// =============================================================================

import (
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/operationfb/accounting-saas/internal/kernel"
	"github.com/operationfb/accounting-saas/token"
)

// Handler is the HTTP boundary for the reports endpoints.
type Handler struct {
	svc *Service
}

// NewHandler builds the Handler.
func NewHandler(svc *Service) *Handler {
	return &Handler{svc: svc}
}

// RegisterRoutes mounts the reports routes behind bearer-token auth. The org comes
// from the token, so there is no id in the path.
func (h *Handler) RegisterRoutes(r *gin.Engine, tokenMaker token.Maker) {
	g := r.Group("/api/v1/reports")
	g.Use(kernel.AuthMiddleware(tokenMaker))
	{
		g.GET("/trial-balance", h.TrialBalance)
		g.GET("/accounts", h.Accounts)
		g.GET("/account-transactions", h.AccountTransactions)
	}
}

// TrialBalance handles GET /api/v1/reports/trial-balance — the trial balance as of
// a date. The optional ?date=YYYY-MM-DD query param sets the snapshot date and
// defaults to today (iteration 1 is a today snapshot). The org/user come from the
// token; any active member may read.
func (h *Handler) TrialBalance(c *gin.Context) {
	asOf := time.Now()
	if raw := c.Query("date"); raw != "" {
		parsed, err := time.Parse("2006-01-02", raw)
		if err != nil {
			kernel.RespondError(c, kernel.ErrBadRequest("date must be in YYYY-MM-DD format", err))
			return
		}
		asOf = parsed
	}

	res, err := h.svc.TrialBalance(c.Request.Context(), kernel.GetAuthUserID(c), kernel.GetAuthOrgID(c), asOf)
	if err != nil {
		kernel.RespondError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"trial_balance": res})
}

// Accounts handles GET /api/v1/reports/accounts — the org's active Chart-of-Accounts
// accounts, backing the Account Transactions report's account picker. The org/user
// come from the token; any active member may read.
func (h *Handler) Accounts(c *gin.Context) {
	res, err := h.svc.Accounts(c.Request.Context(), kernel.GetAuthUserID(c), kernel.GetAuthOrgID(c))
	if err != nil {
		kernel.RespondError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"accounts": res})
}

// AccountTransactions handles GET /api/v1/reports/account-transactions — the GL lines
// for one account over a date range. Query params: account (the nominal code,
// required), from (YYYY-MM-DD, optional → open lower bound), to (YYYY-MM-DD, defaults
// today). The org/user come from the token; any active member may read.
func (h *Handler) AccountTransactions(c *gin.Context) {
	nominal := c.Query("account")
	if nominal == "" {
		kernel.RespondError(c, kernel.ErrBadRequest("account (nominal code) is required", nil))
		return
	}

	var from *time.Time
	if raw := c.Query("from"); raw != "" {
		parsed, err := time.Parse("2006-01-02", raw)
		if err != nil {
			kernel.RespondError(c, kernel.ErrBadRequest("from must be in YYYY-MM-DD format", err))
			return
		}
		from = &parsed
	}

	to := time.Now()
	if raw := c.Query("to"); raw != "" {
		parsed, err := time.Parse("2006-01-02", raw)
		if err != nil {
			kernel.RespondError(c, kernel.ErrBadRequest("to must be in YYYY-MM-DD format", err))
			return
		}
		to = parsed
	}

	// Superseded/reversed activity is hidden by default; ?include_superseded=true
	// reveals the full reversal chain for auditing.
	includeSuperseded := c.Query("include_superseded") == "true"

	res, err := h.svc.AccountTransactions(c.Request.Context(), kernel.GetAuthUserID(c), kernel.GetAuthOrgID(c), nominal, from, to, includeSuperseded)
	if err != nil {
		kernel.RespondError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"account_transactions": res})
}
