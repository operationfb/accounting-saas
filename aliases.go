package main

// aliases.go
// =============================================================================
// TRANSITIONAL alias bridge to internal/kernel.
//
// The shared kernel (errors, HTTP/auth helpers, authz, pg conversions, the tx
// wrapper) moved out of package main into internal/kernel. To avoid touching
// every root file in one go, these aliases re-export the moved symbols under
// their old (unexported) names, so the ~20 root files that still live in
// package main compile UNCHANGED.
//
// This file is a temporary scaffold: as each domain migrates into its own
// internal/<domain> package (using kernel.* directly), the corresponding aliases
// become unused and can be removed. When the last domain leaves package main,
// this file is deleted entirely.
//
// New code MUST NOT rely on these — call internal/kernel directly. The root
// package is wiring-only going forward (see arch_test.go / CLAUDE.md).
// =============================================================================

import kernel "github.com/operationfb/accounting-saas/internal/kernel"

// --- errors (errors.go -> kernel) -------------------------------------------
type (
	AppError  = kernel.AppError
	ErrorCode = kernel.ErrorCode
)

const (
	ErrCodeNotFound             = kernel.ErrCodeNotFound
	ErrCodeValidation           = kernel.ErrCodeValidation
	ErrCodeConflict             = kernel.ErrCodeConflict
	ErrCodeInternal             = kernel.ErrCodeInternal
	ErrCodeForbidden            = kernel.ErrCodeForbidden
	ErrCodePayloadTooLarge      = kernel.ErrCodePayloadTooLarge
	ErrCodeUnsupportedMediaType = kernel.ErrCodeUnsupportedMediaType
	ErrCodeBadRequest           = kernel.ErrCodeBadRequest
)

var (
	ErrNotFound             = kernel.ErrNotFound
	ErrValidation           = kernel.ErrValidation
	ErrBadRequest           = kernel.ErrBadRequest
	ErrConflict             = kernel.ErrConflict
	ErrForbidden            = kernel.ErrForbidden
	ErrPayloadTooLarge      = kernel.ErrPayloadTooLarge
	ErrUnsupportedMediaType = kernel.ErrUnsupportedMediaType
	ErrInternal             = kernel.ErrInternal
	AsAppError              = kernel.AsAppError
)

// --- HTTP/auth helpers (handler_helpers.go + auth_middleware.go -> kernel) ---
var (
	respondError     = kernel.RespondError
	logInternalError = kernel.LogInternalError
	bindJSON         = kernel.BindJSON
	authMiddleware   = kernel.AuthMiddleware
	getAuthUserID    = kernel.GetAuthUserID
	getAuthOrgID     = kernel.GetAuthOrgID
)

// --- authorisation (authz.go + isOrgAdmin -> kernel) ------------------------
var (
	authorizeMember = kernel.AuthorizeMember
	isOrgAdmin      = kernel.IsOrgAdmin
)

// --- pg conversions (the shared subset -> kernel.pgconv) --------------------
var (
	pgNullText           = kernel.NullText
	nullTextToPtr        = kernel.NullTextToPtr
	pgNullInt32          = kernel.NullInt32
	pgInt32FromPtr       = kernel.Int32FromPtr
	timestampToStringPtr = kernel.TimestampToStringPtr
)
