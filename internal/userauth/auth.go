package userauth

// auth.go
// =============================================================================
// Authentication HTTP handlers (login + password reset).
//
// This is the HTTP boundary for auth — it parses the request, talks to the
// sqlc-generated `auth` queries directly (there is no auth service layer),
// verifies the password, mints a PASETO token, and writes a sanitised response.
//
// AuthHandler.RegisterRoutes mounts the PUBLIC /api/v1/auth/* routes (login is how
// a client obtains its token, so these are deliberately NOT behind auth
// middleware). main builds the handler (auth queries + token.Maker + EmailSender +
// TTLs) and calls RegisterRoutes on the shared engine — the per-domain pattern.
// =============================================================================

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"golang.org/x/crypto/bcrypt"

	"github.com/operationfb/accounting-saas/db/auth"
	"github.com/operationfb/accounting-saas/internal/kernel"
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

	// Password-reset dependencies.
	emailSender      EmailSender   // transport for the reset-link email
	appBaseURL       string        // frontend base, used to build the reset link
	passwordResetTTL time.Duration // how long a reset link stays valid (e.g. 15m)
}

// NewAuthHandler wires the dependencies for the auth endpoints.
func NewAuthHandler(
	queries auth.Querier,
	tokenMaker token.Maker,
	accessTokenDuration time.Duration,
	emailSender EmailSender,
	appBaseURL string,
	passwordResetTTL time.Duration,
) *AuthHandler {
	return &AuthHandler{
		queries:             queries,
		tokenMaker:          tokenMaker,
		accessTokenDuration: accessTokenDuration,
		emailSender:         emailSender,
		appBaseURL:          appBaseURL,
		passwordResetTTL:    passwordResetTTL,
	}
}

// RegisterRoutes mounts the PUBLIC auth endpoints on the shared engine. They are
// deliberately NOT behind auth middleware — login is how a client obtains its
// token, and the password-reset flow must work for a logged-out user. Called from
// main on server.Router(), the per-domain registration pattern.
func (h *AuthHandler) RegisterRoutes(r *gin.Engine) {
	g := r.Group("/api/v1/auth")
	{
		g.POST("/login", h.LoginUser)
		g.POST("/forgot-password", h.ForgotPassword)
		g.POST("/reset-password/:token", h.ResetPassword)
	}
}

// =============================================================================
// REQUEST / RESPONSE TYPES
// Kept separate from the sqlc auth.User model so we never leak sensitive
// columns (password_hash, reset tokens, failed_login_count, timestamps, ...).
// =============================================================================

// LoginUserRequest is the JSON body for POST /auth/login.
//
// The login identifier is the user's email — that is the only credential the
// schema supports (the users table has no separate username column, and the
// lookup is GetUserByEmail). `binding:"email"` rejects malformed addresses with
// a 400 before we touch the database.
type LoginUserRequest struct {
	Email    string `json:"email"    binding:"required,email"`
	Password string `json:"password" binding:"required,min=1"`
}

// UserResponse is the safe, public view of a user. It deliberately omits the
// password hash, verification/reset tokens, security counters, the last-login
// IP, and the created_at/updated_at/deleted_at timestamps.
type UserResponse struct {
	ID        string  `json:"id"`
	Email     string  `json:"email"`
	FirstName string  `json:"first_name"`
	LastName  string  `json:"last_name"`
	Phone     *string `json:"phone,omitempty"`
	AvatarURL *string `json:"avatar_url,omitempty"`
	// Payroll-identity fields (future payroll module). All optional/nullable; the
	// login response carries them too, so the SPA's stored user gains them for free.
	NationalInsuranceNumber *string `json:"national_insurance_number,omitempty"`
	UTR                     *string `json:"utr,omitempty"`
	DateOfBirth             *string `json:"date_of_birth,omitempty"` // ISO YYYY-MM-DD
	// Personal/home address (future payroll module). All optional/nullable.
	AddressLine1  *string `json:"address_line_1,omitempty"`
	AddressLine2  *string `json:"address_line_2,omitempty"`
	AddressLine3  *string `json:"address_line_3,omitempty"`
	AddressLine4  *string `json:"address_line_4,omitempty"`
	Postcode      *string `json:"postcode,omitempty"`
	EmailVerified bool    `json:"email_verified"`
}

// OrganisationResponse is the safe public view of the organisation the session
// is scoped to. Neither the org NAME nor its COUNTRY is inside the (encrypted)
// PASETO token — the token only carries the org id — so we surface them here for
// the client. The name is for display (e.g. the top bar); country_code drives
// country-scoped features such as which VAT rates apply, so it is mandatory.
type OrganisationResponse struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	CountryCode string `json:"country_code"` // ISO 3166-1 alpha-2, e.g. 'GB'
	// Role is the caller's membership role IN this organisation (owner/admin/
	// member/accountant/read_only). It is per-membership, not a property of the
	// org itself, but it is scoped to this org and comes from the same
	// ListOrganisationsForUser row — and, like name/country, it is not inside the
	// encrypted PASETO token, so we surface it here for the client to drive
	// role-based UI.
	Role string `json:"role"`
}

// LoginUserResponse is the JSON returned on a successful login: the PASETO
// access token, the sanitised user, and the organisation the session is scoped
// to. organisation is always present on success — login fails if no organisation
// (and therefore no country_code) can be resolved for the user.
type LoginUserResponse struct {
	AccessToken  string                `json:"access_token"`
	User         UserResponse          `json:"user"`
	Organisation *OrganisationResponse `json:"organisation,omitempty"`
}

// NewUserResponse projects a generated auth.User onto the safe UserResponse.
func NewUserResponse(u auth.User) UserResponse {
	return UserResponse{
		ID:                      u.ID.String(),
		Email:                   u.Email,
		FirstName:               u.FirstName,
		LastName:                u.LastName,
		Phone:                   kernel.NullTextToPtr(u.Phone),
		AvatarURL:               kernel.NullTextToPtr(u.AvatarUrl),
		NationalInsuranceNumber: kernel.NullTextToPtr(u.NationalInsuranceNumber),
		UTR:                     kernel.NullTextToPtr(u.Utr),
		DateOfBirth:             kernel.DateToStringPtr(u.DateOfBirth),
		AddressLine1:            kernel.NullTextToPtr(u.AddressLine1),
		AddressLine2:            kernel.NullTextToPtr(u.AddressLine2),
		AddressLine3:            kernel.NullTextToPtr(u.AddressLine3),
		AddressLine4:            kernel.NullTextToPtr(u.AddressLine4),
		Postcode:                kernel.NullTextToPtr(u.Postcode),
		EmailVerified:           u.EmailVerifiedAt.Valid, // verified iff the timestamp is set
	}
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
	var req LoginUserRequest
	if !kernel.BindJSON(c, &req) {
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
		kernel.RespondError(c, err)
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
		kernel.RespondError(c, err)
		return
	}

	// country_code is MANDATORY: the platform is country-scoped (e.g. which VAT
	// rates apply), so every session must carry the organisation's country. A
	// user with no organisation has no country to scope to, and an org-less user
	// can't do anything anyway (authorize() refuses them) — so we fail the login
	// rather than mint a token with no country_code.
	if len(orgs) == 0 {
		kernel.RespondError(c, fmt.Errorf("login: user %s belongs to no organisation; cannot resolve country_code", user.ID))
		return
	}
	defaultOrg := orgs[0] // default to the first active membership

	// country_code is NOT NULL in the schema, so a real row always has one; guard
	// defensively so a blank/corrupt value fails the login loudly instead of
	// silently issuing a session with no country.
	if strings.TrimSpace(defaultOrg.CountryCode) == "" {
		kernel.RespondError(c, fmt.Errorf("login: organisation %s has an empty country_code", defaultOrg.ID))
		return
	}
	orgID := defaultOrg.ID

	// The org name + country are already loaded here, so include them in the
	// response — the client can't read them from the encrypted token.
	org := &OrganisationResponse{
		ID:          defaultOrg.ID.String(),
		Name:        defaultOrg.Name,
		CountryCode: defaultOrg.CountryCode,
		// OrganisationRole is a string-backed enum; convert it to a plain string
		// ("owner"/"admin"/...) for the JSON response.
		Role: string(defaultOrg.Role),
	}

	// Step 6: mint the PASETO token and return it with the safe user view.
	accessToken, err := h.tokenMaker.CreateToken(user.ID, orgID, h.accessTokenDuration)
	if err != nil {
		kernel.RespondError(c, err)
		return
	}

	c.JSON(http.StatusOK, LoginUserResponse{
		AccessToken:  accessToken,
		User:         NewUserResponse(user),
		Organisation: org,
	})
}

// =============================================================================
// PASSWORD RESET
// =============================================================================

// generateToken returns a cryptographically-random, URL-safe token — the raw
// value that travels in the email link. 32 random bytes → base64url (no
// padding), so it is safe as a URL path segment (the alphabet has no '/').
func generateToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// hashToken returns the hex SHA-256 of a raw token. We store only this hash in
// the DB, so a database leak can't be used to reset accounts; lookups hash the
// supplied token and compare. Generic — reusable for email-verification tokens.
func hashToken(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}

// ForgotPasswordRequest is the JSON body for POST /auth/forgot-password.
type ForgotPasswordRequest struct {
	Email string `json:"email" binding:"required,email"`
}

// ResetPasswordRequest is the JSON body for POST /auth/reset-password/:token.
// The token itself comes from the URL path, not the body.
type ResetPasswordRequest struct {
	Password string `json:"password" binding:"required,min=8"`
}

// ForgotPassword handles POST /auth/forgot-password.
//
// It issues a single-use, time-limited reset token, emails a link containing the
// raw token, and ALWAYS returns 200 with a generic message — it never reveals
// whether the email is registered (no account enumeration).
func (h *AuthHandler) ForgotPassword(c *gin.Context) {
	var req ForgotPasswordRequest
	if !kernel.BindJSON(c, &req) {
		return
	}

	ctx := c.Request.Context()
	email := strings.ToLower(strings.TrimSpace(req.Email))

	// The generic response we return in every non-error outcome (sent, unknown
	// email, or inactive account) — so the caller can't tell them apart.
	respondGeneric := func() {
		c.JSON(http.StatusOK, gin.H{"message": "if that email is registered, a password reset link has been sent"})
	}

	rawToken, err := generateToken()
	if err != nil {
		kernel.RespondError(c, err)
		return
	}

	// Store the HASH of the token (the raw token only ever travels in the email).
	// SetPasswordResetToken stamps password_reset_sent_at = now() and returns the
	// user; a missing email yields pgx.ErrNoRows → respond identically.
	user, err := h.queries.SetPasswordResetToken(ctx, auth.SetPasswordResetTokenParams{
		Email:              email,
		PasswordResetToken: pgtype.Text{String: hashToken(rawToken), Valid: true},
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			respondGeneric()
			return
		}
		kernel.RespondError(c, err)
		return
	}

	// Don't email deactivated accounts (still respond generically).
	if !user.IsActive {
		respondGeneric()
		return
	}

	// Build the reset link (token as a path segment) and the email content, then
	// send. A send failure is logged but we STILL return the generic 200 — failing
	// loudly here would reveal that the email exists; the user can re-request.
	resetLink := strings.TrimRight(h.appBaseURL, "/") + "/reset-password/" + rawToken
	subject, body, err := buildPasswordResetEmail(user.FirstName, resetLink, int(h.passwordResetTTL.Minutes()))
	if err != nil {
		kernel.RespondError(c, err)
		return
	}
	if sendErr := h.emailSender.Send(ctx, user.Email, subject, body); sendErr != nil {
		// Still return the generic 200 (don't reveal the email exists), but LOG the
		// failure — otherwise a misconfigured SMTP (bad credentials, unreachable
		// host, ...) is completely silent.
		slog.Error("password reset: failed to send email", "email", user.Email, "err", sendErr)
	}

	respondGeneric()
}

// ResetPassword handles POST /auth/reset-password/:token.
//
// The path token is the raw reset code from the email link. We hash it, look up
// the user, enforce the expiry window (passwordResetTTL), then set the new
// bcrypt password. UpdateUserPassword also clears the token, so a link works
// only once. Any invalid / expired / used token returns the same generic 400.
func (h *AuthHandler) ResetPassword(c *gin.Context) {
	rawToken := c.Param("token")

	var req ResetPasswordRequest
	if !kernel.BindJSON(c, &req) {
		return
	}

	ctx := c.Request.Context()
	const invalidMsg = "invalid or expired reset link"

	user, err := h.queries.GetUserByPasswordResetToken(ctx, pgtype.Text{String: hashToken(rawToken), Valid: true})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			c.JSON(http.StatusBadRequest, gin.H{"error": invalidMsg})
			return
		}
		kernel.RespondError(c, err)
		return
	}

	// Enforce expiry: valid for passwordResetTTL from when the link was issued.
	if !user.PasswordResetSentAt.Valid ||
		time.Now().After(user.PasswordResetSentAt.Time.Add(h.passwordResetTTL)) {
		c.JSON(http.StatusBadRequest, gin.H{"error": invalidMsg})
		return
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(req.Password), 12)
	if err != nil {
		kernel.RespondError(c, err)
		return
	}

	// Sets the new hash and clears password_reset_token + sent_at (single-use).
	if err := h.queries.UpdateUserPassword(ctx, auth.UpdateUserPasswordParams{
		ID:           user.ID,
		PasswordHash: pgtype.Text{String: string(hash), Valid: true},
	}); err != nil {
		kernel.RespondError(c, err)
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "your password has been updated"})
}
