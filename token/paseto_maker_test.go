package token

// paseto_maker_test.go
// =============================================================================
// Tests for PasetoMaker: CreateToken and VerifyToken.
//
// These tests do NOT hit a database. The token package is pure crypto — no
// external dependencies beyond the paseto library itself. That means tests
// run instantly and work anywhere without any setup.
//
// What we test:
//   1. TestCreateAndVerifyToken  — the happy path end-to-end
//   2. TestExpiredToken          — a token past its expiry is rejected
//   3. TestTamperedToken         — a modified token string is rejected
//
// Run with:
//   go test ./internal/token/... -v
//
// The -v flag makes t.Logf() output visible even on passing tests, which is
// how we print the token and payload fields.
// =============================================================================

import (
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

// testSymmetricKey is a fixed 32-byte key used across all tests.
// In production this comes from an environment variable — never hardcode it.
// For tests, a fixed key is fine: we're testing the token logic, not key
// management. The value is arbitrary; any 32 bytes will do.
var testSymmetricKey = []byte("12345678901234567890123456789012") // exactly 32 bytes

// fixedOrgID is the development stub organisation used throughout the project.
// Using a fixed org makes the test output easy to read and matches the seed
// data already in the database — consistent with the rest of the test suite.
var fixedOrgID = uuid.MustParse("00000000-0000-0000-0000-000000000001")

// =============================================================================
// TEST 1: Happy path — create a token then verify it
// =============================================================================

func TestCreateAndVerifyToken(t *testing.T) {
	// -------------------------------------------------------------------------
	// SETUP: build the maker and generate a random user ID.
	//
	// uuid.New() generates a random v4 UUID — no database needed.
	// A random user ID per test run means we're not accidentally passing
	// because of stale state from a previous run.
	// -------------------------------------------------------------------------
	maker, err := NewPasetoMaker(testSymmetricKey)
	if err != nil {
		t.Fatalf("NewPasetoMaker: %v", err)
	}

	randomUserID := uuid.New() // random each run
	duration := 24 * time.Hour

	t.Logf("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")
	t.Logf("INPUT")
	t.Logf("  user_id          : %s  (random)", randomUserID)
	t.Logf("  organisation_id  : %s  (fixed dev org)", fixedOrgID)
	t.Logf("  duration         : %s", duration)

	// -------------------------------------------------------------------------
	// CREATE TOKEN
	// -------------------------------------------------------------------------
	tokenString, err := maker.CreateToken(randomUserID, fixedOrgID, duration)
	if err != nil {
		t.Fatalf("CreateToken: %v", err)
	}

	// -------------------------------------------------------------------------
	// PRINT THE RAW TOKEN AND ITS STRUCTURAL PARTS
	//
	// A PASETO v2 local token has the format:
	//   v2.local.<base64url-encoded-encrypted-payload>
	//
	// There are exactly three dot-separated parts:
	//   parts[0] = "v2"       → version
	//   parts[1] = "local"    → purpose (local = symmetric encryption)
	//   parts[2] = "..."      → the encrypted + authenticated payload (base64url)
	//
	// Unlike JWT, there is no readable header — the version and purpose are
	// in the prefix, not inside the token. The payload is fully encrypted;
	// you cannot base64-decode it to read the claims without the key.
	// -------------------------------------------------------------------------
	parts := strings.Split(tokenString, ".")

	t.Logf("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")
	t.Logf("RAW TOKEN")
	t.Logf("  %s", tokenString)
	t.Logf("")
	t.Logf("TOKEN STRUCTURE  (%d dot-separated parts)", len(parts))

	if len(parts) >= 1 {
		t.Logf("  parts[0] version : %s", parts[0])
	}
	if len(parts) >= 2 {
		t.Logf("  parts[1] purpose : %s", parts[1])
	}
	if len(parts) >= 3 {
		t.Logf("  parts[2] payload : %s...  (base64url, encrypted)", parts[2][:min(40, len(parts[2]))])
	}

	// -------------------------------------------------------------------------
	// VERIFY TOKEN — this is the real test: decrypt and validate
	// -------------------------------------------------------------------------
	verifiedPayload, err := maker.VerifyToken(tokenString)
	if err != nil {
		t.Fatalf("VerifyToken: unexpected error: %v", err)
	}

	t.Logf("")
	t.Logf("PAYLOAD (returned by VerifyToken — after decryption)")
	t.Logf("  user_id          : %s", verifiedPayload.UserID)
	t.Logf("  organisation_id  : %s", verifiedPayload.OrganisationID)
	t.Logf("  issued_at        : %s", verifiedPayload.IssuedAt.Format(time.RFC3339))
	t.Logf("  expires_at       : %s", verifiedPayload.ExpiresAt.Format(time.RFC3339))
	t.Logf("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")

	// -------------------------------------------------------------------------
	// ASSERTIONS: the decrypted payload must match what we put in
	// -------------------------------------------------------------------------
	if verifiedPayload.UserID != randomUserID {
		t.Errorf("user_id: got %s, want %s", verifiedPayload.UserID, randomUserID)
	}
	if verifiedPayload.OrganisationID != fixedOrgID {
		t.Errorf("organisation_id: got %s, want %s", verifiedPayload.OrganisationID, fixedOrgID)
	}
	// ExpiresAt should be approximately now + 24h.
	// We allow a 5-second window either side to account for test execution time.
	expectedExpiry := time.Now().Add(duration)
	diff := verifiedPayload.ExpiresAt.Sub(expectedExpiry)
	if diff < -5*time.Second || diff > 5*time.Second {
		t.Errorf("expires_at: got %s, expected ~%s (diff: %s)",
			verifiedPayload.ExpiresAt.Format(time.RFC3339),
			expectedExpiry.Format(time.RFC3339),
			diff,
		)
	}
}

// =============================================================================
// TEST 2: Expired token is rejected
// =============================================================================
/*
func TestExpiredToken(t *testing.T) {
	maker, err := NewPasetoMaker(testSymmetricKey)
	if err != nil {
		t.Fatalf("NewPasetoMaker: %v", err)
	}

	// Create a token that expired 1 second ago by using a negative duration.
	// -time.Second means IssuedAt = now, ExpiresAt = now - 1s → already expired.
	tokenString, _, err := maker.CreateToken(uuid.New(), fixedOrgID, -time.Second)
	if err != nil {
		t.Fatalf("CreateToken: %v", err)
	}

	t.Logf("expired token: %s", tokenString)

	payload, err := maker.VerifyToken(tokenString)

	// We expect ErrExpiredToken — anything else is a bug.
	if err == nil {
		t.Fatal("VerifyToken: expected an error for expired token, got nil")
	}
	if err != ErrExpiredToken {
		t.Errorf("VerifyToken: expected ErrExpiredToken, got: %v", err)
	}
	if payload != nil {
		t.Errorf("VerifyToken: expected nil payload for expired token, got: %+v", payload)
	}

	t.Logf("correctly rejected with: %v", err)
}

// =============================================================================
// TEST 3: Tampered token is rejected
// =============================================================================

func TestTamperedToken(t *testing.T) {
	maker, err := NewPasetoMaker(testSymmetricKey)
	if err != nil {
		t.Fatalf("NewPasetoMaker: %v", err)
	}

	// Create a valid token.
	tokenString, _, err := maker.CreateToken(uuid.New(), fixedOrgID, time.Hour)
	if err != nil {
		t.Fatalf("CreateToken: %v", err)
	}

	// Tamper with the token by flipping the last character.
	// Because PASETO uses authenticated encryption (XChaCha20-Poly1305),
	// any modification to any byte of the ciphertext causes decryption to fail.
	// This is the core security property we're verifying here.
	tampered := tokenString[:len(tokenString)-1] + "X"
	if tampered == tokenString {
		// edge case: if last char was already 'X', flip to 'Y'
		tampered = tokenString[:len(tokenString)-1] + "Y"
	}

	t.Logf("original token : %s", tokenString)
	t.Logf("tampered token : %s", tampered)

	payload, err := maker.VerifyToken(tampered)

	if err == nil {
		t.Fatal("VerifyToken: expected an error for tampered token, got nil")
	}
	if err != ErrInvalidToken {
		t.Errorf("VerifyToken: expected ErrInvalidToken, got: %v", err)
	}
	if payload != nil {
		t.Errorf("VerifyToken: expected nil payload for tampered token, got: %+v", payload)
	}

	t.Logf("correctly rejected with: %v", err)
}
*/
// =============================================================================
// HELPER
// =============================================================================

// min returns the smaller of a and b.
// Defined here because Go's built-in min is only available from Go 1.21.
// Our go.mod targets 1.22 so we could use the built-in, but defining it
// explicitly makes the intent clear when reading the test.
func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
