package main

// handler_helpers.go
// =============================================================================
// Shared helpers for the HTTP/Gin handler layer.
//
// Every handler ends the same two ways: it either fails (and must turn the error
// into the standard JSON envelope) or it first has to bind+validate the request
// body (and reject a malformed one). Those two steps used to be copy-pasted into
// every handler — ~25 inline error blocks in server.go plus two near-identical
// helpers (writeAppError, respondInternal). These two functions are the single
// home for both, so the boilerplate lives in exactly one place.
// =============================================================================

import (
	"log/slog"

	"github.com/gin-gonic/gin"
)

// respondError writes any error to the client as the standard JSON envelope:
//
//	{ "error": { "code": "...", "message": "..." } }
//
// It is the ONE error-writer for the whole handler layer. AsAppError turns a
// plain error into an *AppError (wrapping unknown errors as ErrInternal), so the
// status code is always derived from the error itself.
//
// Internal (500) errors are the ones we didn't anticipate, so they are LOGGED
// here with the underlying cause and the request that triggered them — the only
// place a 500's real cause is recorded (it is never sent to the client). This
// replaces the old `_ = appErr.Error()` no-op that silently discarded it.
func respondError(c *gin.Context, err error) {
	appErr := AsAppError(err)
	if appErr.Code == ErrCodeInternal {
		logInternalError(c, appErr.Err)
	}
	c.JSON(appErr.HTTPStatus(), gin.H{"error": appErr.ClientResponse()})
}

// logInternalError records an unexpected (500-class) error together with the
// request that triggered it — the only place a 500's real cause is captured (it
// is never sent to the client). Shared by respondError and the few handlers that
// must send a bespoke body but still need the cause logged (e.g. the Mailgun
// webhook, which returns a retry-friendly 500).
func logInternalError(c *gin.Context, cause error) {
	slog.Error("request failed with internal error",
		"method", c.Request.Method,
		"path", c.Request.URL.Path,
		"err", cause,
	)
}

// bindJSON binds and validates the request body into dst. On success it returns
// true. On failure it writes a 400 in the standard error envelope (so a malformed
// request looks like every other error to the client) and returns false, letting
// the handler bail out with the idiomatic:
//
//	if !bindJSON(c, &req) {
//		return
//	}
//
// A binding failure is a *syntactic* problem (bad JSON, missing/!typed field), so
// it is 400 — deliberately distinct from a 422 business-rule ErrValidation. The
// raw binder error is kept only as the (unlogged, unexposed) cause; the client
// gets a clean generic message instead of leaked struct/validator internals.
func bindJSON(c *gin.Context, dst any) bool {
	if err := c.ShouldBindJSON(dst); err != nil {
		respondError(c, ErrBadRequest("invalid request body", err))
		return false
	}
	return true
}
