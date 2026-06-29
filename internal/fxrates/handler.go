package fxrates

// handler.go
// =============================================================================
// The HTTP boundary for the exchange-rate READ API (consumed by the SPA — e.g. the
// invoice form pre-filling a foreign-currency rate). Like internal/currencies, the
// Handler registers its OWN routes and the data is global, so there is no org scope.
//
//   GET /api/v1/exchange-rates?on=YYYY-MM-DD            — all currencies' rates
//   GET /api/v1/exchange-rates/:currency?on=YYYY-MM-DD  — one currency's rate
//
// `on` is optional and defaults to today. Both sit behind bearer-token auth, the
// same as the sibling reference endpoints (/currencies, /vat-rates).
// =============================================================================

import (
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/operationfb/accounting-saas/internal/kernel"
	"github.com/operationfb/accounting-saas/token"
)

// Handler is the HTTP boundary for the exchange-rate read API.
type Handler struct {
	svc *Service
}

// NewHandler builds the Handler.
func NewHandler(svc *Service) *Handler {
	return &Handler{svc: svc}
}

// RegisterRoutes mounts the read routes behind bearer auth.
func (h *Handler) RegisterRoutes(r *gin.Engine, tokenMaker token.Maker) {
	g := r.Group("/api/v1/exchange-rates")
	g.Use(kernel.AuthMiddleware(tokenMaker))
	{
		g.GET("", h.ListRates)
		g.GET("/:currency", h.GetRate)
	}
}

// parseOn reads the optional ?on=YYYY-MM-DD query param, defaulting to today. A
// malformed value is a 400.
func parseOn(c *gin.Context) (time.Time, bool) {
	raw := c.Query("on")
	if raw == "" {
		return time.Now(), true
	}
	on, err := time.Parse("2006-01-02", raw)
	if err != nil {
		kernel.RespondError(c, kernel.ErrBadRequest("on must be a date in YYYY-MM-DD format", err))
		return time.Time{}, false
	}
	return on, true
}

// ListRates handles GET /api/v1/exchange-rates — every currency's most recent rate
// on or before the date.
func (h *Handler) ListRates(c *gin.Context) {
	on, ok := parseOn(c)
	if !ok {
		return
	}
	list, err := h.svc.ListOnDate(c.Request.Context(), on)
	if err != nil {
		kernel.RespondError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"base": h.svc.HomeCurrency(), "rates": list})
}

// GetRate handles GET /api/v1/exchange-rates/:currency — one currency's rate on or
// before the date. A currency we hold no rate for is a 404.
func (h *Handler) GetRate(c *gin.Context) {
	on, ok := parseOn(c)
	if !ok {
		return
	}
	currency := c.Param("currency")
	resp, err := h.svc.Lookup(c.Request.Context(), currency, on)
	if err != nil {
		kernel.RespondError(c, err)
		return
	}
	if resp == nil {
		kernel.RespondError(c, kernel.ErrNotFound("exchange rate", currency))
		return
	}
	c.JSON(http.StatusOK, resp)
}
