package machine

import (
	"errors"
	"time"
)

var (
	ErrNotFound = errors.New("machine or owner not found")
	ErrConflict = errors.New("machine already exists")
)

type Machine struct {
	ID          string
	OwnerUserID string
	Name        string
	CreatedAt   time.Time
}
