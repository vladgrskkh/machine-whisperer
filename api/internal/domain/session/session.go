package session

import (
	"errors"
	"time"
)

var ErrNotFound = errors.New("session or refresh token not found")

type Session struct {
	ID, UserID           string
	CreatedAt, ExpiresAt time.Time
	RevokedAt            *time.Time
}

type RefreshToken struct {
	Hash       [32]byte
	SessionID  string
	CreatedAt  time.Time
	ConsumedAt *time.Time
}

type AccessClaims struct {
	UserID, SessionID, JTI string
	IssuedAt, ExpiresAt    time.Time
}
