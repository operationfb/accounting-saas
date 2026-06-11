package token

// paseto_maker.go
// =============================================================================
// PasetoMaker implements the Maker interface using PASETO v2 local tokens.
//
// LIBRARY: github.com/o1egl/paseto
//   go get github.com/o1egl/paseto
//
// HOW THIS LIBRARY WORKS:
//   1. Create a V2 instance:        v2 := paseto.NewV2()
//   2. Encrypt a payload:           token, err := v2.Encrypt(key, payload, footer)
//   3. Decrypt back to a struct:    err := v2.Decrypt(token, key, &payload, &footer)
//
//   The payload can be any JSON-serialisable struct. We use paseto.JSONToken
//   (the library's built-in claims type) because it already has Expiration and
//   IssuedAt fields, and a Set() method for adding custom claims like user_id
//   and organisation_id.
//
// KEY:
//   v2.Encrypt takes a plain []byte key that must be exactly 32 bytes.
//   Generate one with: openssl rand -hex 16
//   ("hex 16" → 16 hex bytes → when decoded = 32 raw bytes)
//   Store in .env as PASETO_SYMMETRIC_KEY. Never commit to source control.
// =============================================================================

import (
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/o1egl/paseto"
)

// symmetricKeySize is the exact byte length required by PASETO v2 local.
// XChaCha20-Poly1305 requires a 256-bit (32-byte) key.
const symmetricKeySize = 32

// PasetoMaker is the concrete implementation of Maker.
// Create one instance at server startup and reuse it — it is goroutine-safe.
type PasetoMaker struct {
	// paseto is the V2 protocol instance. Stateless; safe to share.
	paseto *paseto.V2

	// symmetricKey is the raw 32-byte secret. Keep it private.
	symmetricKey []byte
}

// NewPasetoMaker creates a PasetoMaker from a 32-byte symmetric key.
//
// Usage in main.go / server setup:
//
//	key := []byte(os.Getenv("PASETO_SYMMETRIC_KEY")) // must be 32 bytes
//	maker, err := token.NewPasetoMaker(key)
func NewPasetoMaker(symmetricKey []byte) (*PasetoMaker, error) {
	if len(symmetricKey) != symmetricKeySize {
		return nil, fmt.Errorf(
			"invalid key size: PASETO v2 requires exactly %d bytes, got %d",
			symmetricKeySize,
			len(symmetricKey),
		)
	}

	return &PasetoMaker{
		paseto:       paseto.NewV2(),
		symmetricKey: symmetricKey,
	}, nil
}

func (maker *PasetoMaker) CreateToken(userID uuid.UUID, orgID uuid.UUID, duration time.Duration) (string, error) {
	payload, err := NewPayload(userID, orgID, duration)
	if err != nil {
		return "", err
	}

	return maker.paseto.Encrypt(maker.symmetricKey, payload, nil) // this also checks the keysize. No need in the constructor
}

/*
	func (maker *PasetoMaker) CreateToken(userID uuid.UUID, orgID uuid.UUID, duration time.Duration) (string, *Payload, error) {
		// Step 1: Build our Payload (the data we want to embed in the token).
		payload, err := NewPayload(userID, orgID, duration)
		if err != nil {
			return "", nil, fmt.Errorf("failed to create payload: %w", err)
		}

		// Step 2: Populate a paseto.JSONToken with our claims.
		//
		// paseto.JSONToken is the library's built-in claims struct. It has:
		//   - Expiration time.Time  → the standard "exp" claim
		//   - IssuedAt   time.Time  → the standard "iat" claim
		//   - Set(key, value)       → stores any extra claim as JSON
		//
		// We store user_id and organisation_id as string claims using Set().
		// UUIDs are stored as strings because JSON has no UUID type.
		jsonToken := paseto.JSONToken{
			IssuedAt:   payload.IssuedAt,
			Expiration: payload.ExpiresAt,
		}
		jsonToken.Set("user_id", payload.UserID.String())
		jsonToken.Set("organisation_id", payload.OrganisationID.String())

		// Step 3: Encrypt the token.
		// Encrypt(key, payload, footer) JSON-encodes the payload then applies
		// XChaCha20-Poly1305. The footer is additional cleartext metadata
		// attached to the token (useful for key rotation hints). We don't need
		// it yet so we pass an empty string.
		tokenString, err := maker.paseto.Encrypt(maker.symmetricKey, jsonToken, "")
		if err != nil {
			return "", nil, fmt.Errorf("failed to encrypt token: %w", err)
		}

		return tokenString, payload, nil
	}
*/
func (maker *PasetoMaker) VerifyToken(token string) (*Payload, error) {
	payload := &Payload{}

	fmt.Printf("[DEBUG] tokestring    = %s\n", token)
	err := maker.paseto.Decrypt(token, maker.symmetricKey, payload, nil)
	if err != nil {
		return nil, ErrInvalidToken
	}
	fmt.Printf("[DEBUG] payload.ExpiresAt = %s\n", payload.ExpiresAt.Format(time.RFC3339Nano))
	fmt.Printf("[DEBUG] payload.UserID    = %s\n", payload.UserID)
	fmt.Printf("[DEBUG] payload.OrgID     = %s\n", payload.OrganisationID)
	if time.Now().After(payload.ExpiresAt) {
		return nil, ErrExpiredToken
	}

	return payload, nil
}

// VerifyToken decrypts and validates a PASETO v2 local token string.
//
// The library calls json.Unmarshal into our Payload struct on decryption,
// so all fields are restored exactly as set during CreateToken.
//
// Returns ErrInvalidToken if the token is tampered/malformed/wrong-key,
// or ErrExpiredToken if the token is genuine but past its expiry.
/*
func (maker *PasetoMaker) VerifyToken(tokenString string) (*Payload, error) {
	var payload Payload
	var footer string // required by Decrypt signature; we don't use it

	// Decrypt fills payload via json.Unmarshal.
	// Returns an error if the token is malformed or encrypted with a different key.
	if err := maker.paseto.Decrypt(tokenString, maker.symmetricKey, &payload, &footer); err != nil {
		return nil, ErrInvalidToken
	}

	// Check expiry. Valid() returns ErrExpiredToken if time.Now() > ExpiresAt.
	if err := payload.Valid(); err != nil {
		return nil, err
	}

	return &payload, nil
}

func (maker *PasetoMaker) VerifyToken(tokenString string) (*Payload, error) {
	// Decrypt into raw JSON — captures exactly what was encrypted.
	var rawJSON json.RawMessage
	var footer string

	if err := maker.paseto.Decrypt(tokenString, maker.symmetricKey, &rawJSON, &footer); err != nil {
		return nil, ErrInvalidToken
	}

	// Unmarshal into Payload ourselves — full control over field mapping.
	var payload Payload
	if err := json.Unmarshal(rawJSON, &payload); err != nil {
		return nil, ErrInvalidToken
	}

	if err := payload.Valid(); err != nil {
		return nil, err
	}

	return &payload, nil
}
*/
