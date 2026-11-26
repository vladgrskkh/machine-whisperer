package device

import (
	"context"
	"crypto/subtle"
	"log/slog"
	"time"

	"go.opentelemetry.io/otel/trace"

	"github.com/vladgrskkh/machine-whisperer/api/internal/domain/device"
	"github.com/vladgrskkh/machine-whisperer/api/internal/spanutil"
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
	tracer       trace.Tracer
	logger       *slog.Logger
	repository   ClaimRepository
	transactions TransactionManager
}

func NewClaim(
	repository ClaimRepository, transactions TransactionManager, logger *slog.Logger, tracer trace.Tracer,
) *Claim {
	return &Claim{tracer: tracer, logger: logger, repository: repository, transactions: transactions}
}

func (c *Claim) Claim(ctx context.Context, deviceID, machineID, ownerUserID string, code device.Secret) (err error) {
	ctx, span := c.tracer.Start(ctx, "device.claim")
	defer func() { spanutil.End(span, err, device.ErrClaimUnavailable) }()
	verifier := code.Verifier()
	err = c.transactions.WithinTransaction(ctx, func(txCtx context.Context) error {
		value, findErr := c.repository.FindForClaim(txCtx, deviceID)
		if findErr != nil {
			return findErr
		}
		if subtle.ConstantTimeCompare(verifier[:], value.ClaimVerifier[:]) != 1 {
			return device.ErrClaimUnavailable
		}
		if lockErr := c.repository.LockMachineOwner(txCtx, machineID, ownerUserID); lockErr != nil {
			return lockErr
		}
		// Waiting on either row can take the claim past expiry.
		now := time.Now().UTC()
		if !now.Before(value.ClaimExpiresAt) {
			return device.ErrClaimUnavailable
		}
		return c.repository.Claim(txCtx, deviceID, machineID, ownerUserID, now)
	})
	if err == nil {
		c.logger.InfoContext(ctx, "device claimed", "machine_id", machineID, "device_id", deviceID)
	}
	return err
}
