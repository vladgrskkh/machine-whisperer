package device

import (
	"context"
	"fmt"
	"time"

	"github.com/vladgrskkh/machine-whisperer/api/internal/domain/device"
)

type ProvisionRepository interface {
	Create(context.Context, device.Device) error
}

type IDGenerator interface {
	Generate(context.Context) (string, error)
}

type SecretGenerator interface {
	Generate(context.Context) (device.Secret, error)
}

type Provision struct {
	repository ProvisionRepository
	ids        IDGenerator
	apiSecrets SecretGenerator
	claimCodes SecretGenerator
	claimTTL   time.Duration
}

type Provisioned struct {
	DeviceID  string
	APISecret device.Secret
	ClaimCode device.Secret
}

func NewProvision(
	repository ProvisionRepository,
	ids IDGenerator,
	apiSecrets, claimCodes SecretGenerator,
	claimTTL time.Duration,
) *Provision {
	return &Provision{
		repository: repository,
		ids:        ids,
		apiSecrets: apiSecrets,
		claimCodes: claimCodes,
		claimTTL:   claimTTL,
	}
}

func (p *Provision) Provision(ctx context.Context) (Provisioned, error) {
	id, err := p.ids.Generate(ctx)
	if err != nil {
		return Provisioned{}, fmt.Errorf("generate device ID: %w", err)
	}
	apiSecret, err := p.apiSecrets.Generate(ctx)
	if err != nil {
		return Provisioned{}, fmt.Errorf("generate device API credential: %w", err)
	}
	claimCode, err := p.claimCodes.Generate(ctx)
	if err != nil {
		return Provisioned{}, fmt.Errorf("generate device claim code: %w", err)
	}
	now := time.Now().UTC()
	value := device.Device{
		ID:             id,
		KeyVerifier:    apiSecret.Verifier(),
		ClaimVerifier:  claimCode.Verifier(),
		CreatedAt:      now,
		ClaimExpiresAt: now.Add(p.claimTTL),
	}
	if err = p.repository.Create(ctx, value); err != nil {
		return Provisioned{}, fmt.Errorf("provision device: %w", err)
	}
	return Provisioned{DeviceID: id, APISecret: apiSecret, ClaimCode: claimCode}, nil
}
