package device

import (
	"context"
	"crypto/subtle"
	"time"

	"github.com/vladgrskkh/machine-whisperer/api/internal/domain/device"
)

type ClaimRepository interface {
	FindForClaim(context.Context, string) (device.Device, error)
	LockMachineOwner(ctx context.Context, machineID, ownerUserID string) error
	Claim(ctx context.Context, deviceID, machineID, ownerUserID string, at time.Time) error
}

type TransactionManager interface {
	WithinTransaction(context.Context, func(context.Context) error) error
}

type Claim struct {
	repository   ClaimRepository
	transactions TransactionManager
}

func NewClaim(repository ClaimRepository, transactions TransactionManager) *Claim {
	return &Claim{repository: repository, transactions: transactions}
}

func (c *Claim) Claim(ctx context.Context, deviceID, machineID, ownerUserID string, code device.Secret) error {
	verifier := code.Verifier()
	return c.transactions.WithinTransaction(ctx, func(txCtx context.Context) error {
		value, err := c.repository.FindForClaim(txCtx, deviceID)
		if err != nil {
			return err
		}
		if subtle.ConstantTimeCompare(verifier[:], value.ClaimVerifier[:]) != 1 {
			return device.ErrClaimUnavailable
		}
		if err = c.repository.LockMachineOwner(txCtx, machineID, ownerUserID); err != nil {
			return err
		}
		// Waiting on either row can take the claim past expiry.
		now := time.Now().UTC()
		if !now.Before(value.ClaimExpiresAt) {
			return device.ErrClaimUnavailable
		}
		return c.repository.Claim(txCtx, deviceID, machineID, ownerUserID, now)
	})
}
