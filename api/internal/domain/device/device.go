package device

import (
	"errors"
	"time"
)

const RoleTelemetryWriter = "telemetry_writer"

var (
	ErrInvalidCredentials  = errors.New("invalid device credentials")
	ErrUnavailable         = errors.New("device unavailable")
	ErrClaimUnavailable    = errors.New("device claim unavailable")
	ErrConflict            = errors.New("device conflicts with an existing enrollment")
	ErrTransactionRequired = errors.New("device claim requires a transaction")
)

type Device struct {
	ID                  string
	MachineID           *string
	ProvisionedByUserID *string
	KeyVerifier         [32]byte
	ClaimVerifier       [32]byte
	ClaimExpiresAt      time.Time
	ClaimedAt           *time.Time
	RevokedAt           *time.Time
	CreatedAt           time.Time
}
