package token

import (
	"time"

	"github.com/google/uuid"
)

type Maker interface {
	CreateToken(userID uuid.UUID, orgID uuid.UUID, duration time.Duration) (string, error)
	VerifyToken(token string) (*Payload, error)
}
