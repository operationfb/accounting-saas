package main

// errors.go
// =============================================================================
// AppError — a structured error type for the application layer.
//
// The problem with plain fmt.Errorf:
//   fmt.Errorf("expense not found") gives the handler no information about
//   WHAT KIND of error occurred. The handler has to either send 500 for
//   everything, or do brittle string matching ("if err.Error() == ...").
//   Both are bad.
//
// The solution — a typed error:
//   AppError carries an error Code (a string constant like "not_found") that
//   the handler can switch on to decide the HTTP status, plus a human-readable
//   Message for the API response body, plus an optional internal Err for
//   logging the root cause without exposing it to the client.
//
// Usage pattern:
//   Service returns:  ErrNotFound("expense", id)
//   Handler receives: *AppError{Code: "not_found", Message: "expense abc not found"}
//   Handler sends:    HTTP 404 {"error": {"code": "not_found", "message": "..."}}
// =============================================================================

import (
	"errors"
	"fmt"
	"net/http"
)

// ErrorCode is a string constant identifying the category of error.
// Using a named type (not raw string) means the compiler catches typos:
// ErrCodeNotFound vs "not_foudn" — the former won't compile if misspelled.
type ErrorCode string

const (
	// ErrCodeNotFound — the requested resource does not exist.
	// Maps to HTTP 404.
	ErrCodeNotFound ErrorCode = "not_found"

	// ErrCodeValidation — the request was malformed or failed a business rule.
	// Maps to HTTP 422. We use 422 (Unprocessable Entity) rather than 400
	// (Bad Request) for business rule violations: 400 means the request was
	// syntactically wrong; 422 means we understood it but couldn't process it.
	ErrCodeValidation ErrorCode = "validation_error"

	// ErrCodeConflict — the request conflicts with existing state.
	// Example: submitting an expense that is already submitted.
	// Maps to HTTP 409.
	ErrCodeConflict ErrorCode = "conflict"

	// ErrCodeInternal — an unexpected error we didn't anticipate.
	// Maps to HTTP 500. We never send internal error details to the client.
	ErrCodeInternal ErrorCode = "internal_error"

	// ErrCodeForbidden — the caller is authenticated but not allowed to perform
	// this action or access this resource. Maps to HTTP 403.
	ErrCodeForbidden ErrorCode = "forbidden"

	// ErrCodePayloadTooLarge — the request body (e.g. an uploaded file) exceeds
	// the allowed size. Maps to HTTP 413.
	ErrCodePayloadTooLarge ErrorCode = "payload_too_large"

	// ErrCodeUnsupportedMediaType — the uploaded file is of a type we don't
	// accept (we only allow PDF/JPEG/PNG receipts). Maps to HTTP 415.
	ErrCodeUnsupportedMediaType ErrorCode = "unsupported_media_type"
)

// AppError is the structured error type returned by the service layer.
//
// Fields:
//
//	Code    — machine-readable category (used by the handler to pick HTTP status)
//	Message — human-readable explanation safe to send to the API client
//	Err     — the underlying technical error, for server-side logging only
//	          (never sent to the client)
type AppError struct {
	Code    ErrorCode
	Message string
	Err     error // internal cause — logged, not exposed
}

// Error implements the built-in `error` interface. This is what makes
// *AppError usable anywhere a plain `error` is expected.
// We include the internal error in the string so it appears in server logs.
func (e *AppError) Error() string {
	if e.Err != nil {
		return fmt.Sprintf("[%s] %s: %v", e.Code, e.Message, e.Err)
	}
	return fmt.Sprintf("[%s] %s", e.Code, e.Message)
}

// Unwrap allows errors.Is() and errors.As() to inspect the wrapped Err.
// This is part of Go's error wrapping convention (introduced in Go 1.13).
// Example: errors.Is(appErr, pgx.ErrNoRows) will work if Err wraps pgx.ErrNoRows.
func (e *AppError) Unwrap() error {
	return e.Err
}

// HTTPStatus maps an ErrorCode to the appropriate HTTP status code.
// The handler calls this to decide what status to send without containing
// any knowledge of the error code meanings itself.
func (e *AppError) HTTPStatus() int {
	switch e.Code {
	case ErrCodeNotFound:
		return http.StatusNotFound // 404
	case ErrCodeValidation:
		return http.StatusUnprocessableEntity // 422
	case ErrCodeConflict:
		return http.StatusConflict // 409
	case ErrCodeForbidden:
		return http.StatusForbidden // 403
	case ErrCodePayloadTooLarge:
		return http.StatusRequestEntityTooLarge // 413
	case ErrCodeUnsupportedMediaType:
		return http.StatusUnsupportedMediaType // 415
	case ErrCodeInternal:
		return http.StatusInternalServerError // 500
	default:
		return http.StatusInternalServerError // safe fallback
	}
}

// =============================================================================
// CONSTRUCTORS
// Shorthand functions so the service layer reads naturally.
// Each one takes a human message and an optional underlying error.
// =============================================================================

// ErrNotFound constructs a "not_found" AppError.
// resource is the entity type ("expense", "category") and id is its identifier.
// This gives a consistent message format: "expense abc-123 not found"
func ErrNotFound(resource, id string) *AppError {
	return &AppError{
		Code:    ErrCodeNotFound,
		Message: fmt.Sprintf("%s %s not found", resource, id),
	}
}

// ErrValidation constructs a "validation_error" AppError.
// msg should be a clear explanation the client can act on,
// e.g. "dated_on cannot be more than 1 year in the past".
// cause is the underlying technical error (e.g. from a parser) — can be nil.
func ErrValidation(msg string, cause error) *AppError {
	return &AppError{
		Code:    ErrCodeValidation,
		Message: msg,
		Err:     cause,
	}
}

// ErrConflict constructs a "conflict" AppError.
// msg describes the conflict, e.g. "expense is already submitted".
func ErrConflict(msg string) *AppError {
	return &AppError{
		Code:    ErrCodeConflict,
		Message: msg,
	}
}

// ErrForbidden constructs a "forbidden" AppError.
// msg explains what was denied, e.g. "you do not have access to this expense".
func ErrForbidden(msg string) *AppError {
	return &AppError{
		Code:    ErrCodeForbidden,
		Message: msg,
	}
}

// ErrPayloadTooLarge constructs a "payload_too_large" AppError.
// msg should state the limit, e.g. "file is 26214400 bytes; the limit is 20971520".
func ErrPayloadTooLarge(msg string) *AppError {
	return &AppError{
		Code:    ErrCodePayloadTooLarge,
		Message: msg,
	}
}

// ErrUnsupportedMediaType constructs an "unsupported_media_type" AppError.
// msg names the rejected type, e.g. `file type "text/plain" is not allowed`.
func ErrUnsupportedMediaType(msg string) *AppError {
	return &AppError{
		Code:    ErrCodeUnsupportedMediaType,
		Message: msg,
	}
}

// ErrInternal constructs an "internal_error" AppError.
// msg is a safe generic message for the client ("unexpected error occurred").
// cause is the real error, which the handler will log but not send to the client.
func ErrInternal(cause error) *AppError {
	return &AppError{
		Code:    ErrCodeInternal,
		Message: "an unexpected error occurred",
		Err:     cause,
	}
}

// =============================================================================
// HANDLER HELPER
// =============================================================================

// AsAppError attempts to cast any error to *AppError.
// If the error is already an *AppError, it returns it directly.
// If it is a plain error (e.g. from a library), it wraps it in ErrInternal.
//
// This means the handler only ever deals with *AppError — it never needs to
// type-assert or check for nil in two different ways.
//
// Usage in handlers:
//
//	appErr := AsAppError(err)
//	c.JSON(appErr.HTTPStatus(), gin.H{"error": appErr.ClientResponse()})
func AsAppError(err error) *AppError {
	var appErr *AppError
	// errors.As walks the error chain (via Unwrap) looking for *AppError.
	// This means it works even if the AppError was wrapped by fmt.Errorf("%w").
	if errors.As(err, &appErr) {
		return appErr
	}
	// Unknown error type — wrap it so callers always get *AppError back.
	return ErrInternal(err)
}

// ClientResponse returns the JSON-safe representation of the error.
// This is what gets sent to the API client — it deliberately excludes
// the internal Err field so implementation details are never leaked.
//
// Response shape:
//
//	{ "error": { "code": "not_found", "message": "expense abc not found" } }
func (e *AppError) ClientResponse() map[string]string {
	return map[string]string{
		"code":    string(e.Code),
		"message": e.Message,
	}
}
