package token

import (
	"errors"
	"time"

	"github.com/google/uuid"
)

var (
	// ErrExpiredToken is returned when the token's ExpiresAt is in the past.
	ErrExpiredToken = errors.New("token has expired")

	// ErrInvalidToken is returned when the token cannot be decrypted or parsed —
	// i.e. it was tampered with, encrypted with a different key, or malformed.
	ErrInvalidToken = errors.New("token is invalid")
)

type Payload struct {
	UserID         uuid.UUID
	OrganisationID uuid.UUID
	IssuedAt       time.Time
	ExpiresAt      time.Time
}

func NewPayload(userID uuid.UUID, orgID uuid.UUID, duration time.Duration) (*Payload, error) {
	return &Payload{
		UserID:         userID,
		OrganisationID: orgID,
		IssuedAt:       time.Now(),
		ExpiresAt:      time.Now().Add(duration),
	}, nil
}

func (p *Payload) Valid() error {
	now := time.Now().UTC()
	expires := p.ExpiresAt.UTC()

	if now.After(expires) {
		return ErrExpiredToken
	}
	return nil
}
