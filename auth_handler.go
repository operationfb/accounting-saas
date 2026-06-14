package main

// auth_handler.go
// =============================================================================
// Authentication HTTP handlers.
//
// This file is the HTTP boundary for auth — it parses the request, talks to the
// sqlc-generated `auth` queries directly (there is no auth service layer yet),
// verifies the password, mints a PASETO token, and writes a sanitised response.
//
// NOTE: this handler is intentionally NOT wired into the router yet. Construct
// an *AuthHandler in main.go (it needs the auth queries, a token.Maker, and an
// access-token TTL) and register LoginUser on a route, e.g.:
//
//	authQueries := auth.New(pool)
//	authHandler := NewAuthHandler(authQueries, tokenMaker, 15*time.Minute)
//	v1.POST("/auth/login", authHandler.LoginUser)
// =============================================================================

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"golang.org/x/crypto/bcrypt"

	"github.com/operationfb/accounting-saas/db/auth"
	"github.com/operationfb/accounting-saas/token"
)

// AuthHandler holds the dependencies the auth endpoints need.
//
// It depends on the generated auth.Querier interface (not the concrete
// *auth.Queries) so handler tests can pass a mock implementation instead of a
// real database connection.
type AuthHandler struct {
	queries    auth.Querier
	tokenMaker token.Maker

	// accessTokenDuration is how long an issued token stays valid. Injected
	// rather than hardcoded so it can be configured per environment.
	accessTokenDuration time.Duration
}

// NewAuthHandler wires the dependencies for the auth endpoints.
func NewAuthHandler(queries auth.Querier, tokenMaker token.Maker, accessTokenDuration time.Duration) *AuthHandler {
	return &AuthHandler{
		queries:             queries,
		tokenMaker:          tokenMaker,
		accessTokenDuration: accessTokenDuration,
	}
}

// =============================================================================
// REQUEST / RESPONSE TYPES
// Kept separate from the sqlc auth.User model so we never leak sensitive
// columns (password_hash, reset tokens, failed_login_count, timestamps, ...).
// =============================================================================

// loginUserRequest is the JSON body for POST /auth/login.
//
// The login identifier is the user's email — that is the only credential the
// schema supports (the users table has no separate username column, and the
// lookup is GetUserByEmail). `binding:"email"` rejects malformed addresses with
// a 400 before we touch the database.
type loginUserRequest struct {
	Email    string `json:"email"    binding:"required,email"`
	Password string `json:"password" binding:"required,min=1"`
}

// userResponse is the safe, public view of a user. It deliberately omits the
// password hash, verification/reset tokens, security counters, the last-login
// IP, and the created_at/updated_at/deleted_at timestamps.
type userResponse struct {
	ID            string  `json:"id"`
	Email         string  `json:"email"`
	FirstName     string  `json:"first_name"`
	LastName      string  `json:"last_name"`
	Phone         *string `json:"phone,omitempty"`
	AvatarURL     *string `json:"avatar_url,omitempty"`
	EmailVerified bool    `json:"email_verified"`
}

// organisationResponse is the safe public view of the organisation the session
// is scoped to. Neither the org NAME nor its COUNTRY is inside the (encrypted)
// PASETO token — the token only carries the org id — so we surface them here for
// the client. The name is for display (e.g. the top bar); country_code drives
// country-scoped features such as which VAT rates apply, so it is mandatory.
type organisationResponse struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	CountryCode string `json:"country_code"` // ISO 3166-1 alpha-2, e.g. 'GB'
}

// loginUserResponse is the JSON returned on a successful login: the PASETO
// access token, the sanitised user, and the organisation the session is scoped
// to. organisation is always present on success — login fails if no organisation
// (and therefore no country_code) can be resolved for the user.
type loginUserResponse struct {
	AccessToken  string                `json:"access_token"`
	User         userResponse          `json:"user"`
	Organisation *organisationResponse `json:"organisation,omitempty"`
}

// newUserResponse projects a generated auth.User onto the safe userResponse.
func newUserResponse(u auth.User) userResponse {
	return userResponse{
		ID:            u.ID.String(),
		Email:         u.Email,
		FirstName:     u.FirstName,
		LastName:      u.LastName,
		Phone:         textOrNil(u.Phone),
		AvatarURL:     textOrNil(u.AvatarUrl),
		EmailVerified: u.EmailVerifiedAt.Valid, // verified iff the timestamp is set
	}
}

// textOrNil converts a nullable pgtype.Text into a *string (nil when NULL) so
// absent optional fields are omitted from the JSON instead of serialised as "".
func textOrNil(t pgtype.Text) *string {
	if !t.Valid {
		return nil
	}
	s := t.String
	return &s
}

// =============================================================================
// HANDLER
// =============================================================================

// LoginUser handles POST /auth/login.
//
// Flow:
//  1. Parse and validate {email, password}.
//  2. Look up the user by email (sqlc GetUserByEmail).
//  3. Reject disabled, locked, or OAuth-only (no password) accounts.
//  4. Verify the password against the stored bcrypt hash.
//  5. Pick the organisation to scope the token to (first active membership).
//  6. Mint a PASETO token and return it with the sanitised user.
//
// Credential failures all return the same generic 401 ("invalid email or
// password") so the endpoint can't be used to discover which emails exist.
func (h *AuthHandler) LoginUser(c *gin.Context) {
	// Step 1: parse and validate the body. A bad body is a 400 (same pattern as
	// the expense handlers).
	var req loginUserRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	ctx := c.Request.Context()

	// Normalise the email the way the schema stores it (trimmed + lowercase)
	// so the lookup matches regardless of how the client cased it.
	email := strings.ToLower(strings.TrimSpace(req.Email))

	// Step 2: fetch the user.
	user, err := h.queries.GetUserByEmail(ctx, email)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			// Unknown email — return the generic message (no enumeration).
			c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid email or password"})
			return
		}
		// Anything else is an unexpected server/database error.
		respondInternal(c, err)
		return
	}

	// Step 3a: a deactivated account cannot log in. Generic message so we don't
	// reveal that the email is registered.
	if !user.IsActive {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid email or password"})
		return
	}

	// Step 3b: a locked account is refused until the lockout expires. This is
	// the one case where we say more, so the user understands why a correct
	// password still won't work.
	if user.LockedUntil.Valid && user.LockedUntil.Time.After(time.Now()) {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "account is temporarily locked, please try again later"})
		return
	}

	// Step 3c: OAuth-only accounts have no password hash and cannot use the
	// email/password flow.
	if !user.PasswordHash.Valid {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid email or password"})
		return
	}

	// Step 4: verify the password. CompareHashAndPassword is constant-time and
	// returns a non-nil error on mismatch.
	if err := bcrypt.CompareHashAndPassword([]byte(user.PasswordHash.String), []byte(req.Password)); err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid email or password"})
		return
	}

	// Step 5: choose the organisation to embed in the token. A user may belong
	// to several organisations; we default to their first active membership.
	// A future "switch organisation" endpoint can re-mint a token for a
	// different org.
	//
	// ListOrganisationsForUser does SELECT o.* so each row already carries the
	// organisation's country_code.
	orgs, err := h.queries.ListOrganisationsForUser(ctx, user.ID)
	if err != nil {
		respondInternal(c, err)
		return
	}

	// country_code is MANDATORY: the platform is country-scoped (e.g. which VAT
	// rates apply), so every session must carry the organisation's country. A
	// user with no organisation has no country to scope to, and an org-less user
	// can't do anything anyway (authorize() refuses them) — so we fail the login
	// rather than mint a token with no country_code.
	if len(orgs) == 0 {
		respondInternal(c, fmt.Errorf("login: user %s belongs to no organisation; cannot resolve country_code", user.ID))
		return
	}
	defaultOrg := orgs[0] // default to the first active membership

	// country_code is NOT NULL in the schema, so a real row always has one; guard
	// defensively so a blank/corrupt value fails the login loudly instead of
	// silently issuing a session with no country.
	if strings.TrimSpace(defaultOrg.CountryCode) == "" {
		respondInternal(c, fmt.Errorf("login: organisation %s has an empty country_code", defaultOrg.ID))
		return
	}
	orgID := defaultOrg.ID

	// The org name + country are already loaded here, so include them in the
	// response — the client can't read them from the encrypted token.
	org := &organisationResponse{
		ID:          defaultOrg.ID.String(),
		Name:        defaultOrg.Name,
		CountryCode: defaultOrg.CountryCode,
	}

	// Step 6: mint the PASETO token and return it with the safe user view.
	accessToken, err := h.tokenMaker.CreateToken(user.ID, orgID, h.accessTokenDuration)
	if err != nil {
		respondInternal(c, err)
		return
	}

	c.JSON(http.StatusOK, loginUserResponse{
		AccessToken:  accessToken,
		User:         newUserResponse(user),
		Organisation: org,
	})
}

// respondInternal logs (placeholder) and returns a 500 without leaking the
// underlying cause, reusing the AppError machinery from errors.go.
func respondInternal(c *gin.Context, err error) {
	appErr := ErrInternal(err)
	_ = appErr.Error() // TODO: replace with structured logger (slog/zap)
	c.JSON(appErr.HTTPStatus(), gin.H{"error": appErr.ClientResponse()})
}
