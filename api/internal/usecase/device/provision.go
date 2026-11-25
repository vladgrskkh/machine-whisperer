package device

import (
	"context"
	"fmt"
	"time"

	"go.opentelemetry.io/otel/trace"

	"github.com/vladgrskkh/machine-whisperer/api/internal/domain/device"
	"github.com/vladgrskkh/machine-whisperer/api/internal/spanutil"
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
	tracer     trace.Tracer
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
	tracer trace.Tracer,
) *Provision {
	return &Provision{
		tracer: tracer, repository: repository,
		ids:        ids,
		apiSecrets: apiSecrets,
		claimCodes: claimCodes,
		claimTTL:   claimTTL,
	}
}

//nolint:nonamedreturns // Span completion observes the returned error.
func (p *Provision) Provision(ctx context.Context, provisionerID string) (_ Provisioned, err error) {
	ctx, span := p.tracer.Start(ctx, "device.provision")
	defer func() { spanutil.End(span, err) }()
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
		ID:                  id,
		ProvisionedByUserID: &provisionerID,
		KeyVerifier:         apiSecret.Verifier(),
		ClaimVerifier:       claimCode.Verifier(),
		CreatedAt:           now,
		ClaimExpiresAt:      now.Add(p.claimTTL),
	}
	if err = p.repository.Create(ctx, value); err != nil {
		return Provisioned{}, fmt.Errorf("provision device: %w", err)
	}
	return Provisioned{DeviceID: id, APISecret: apiSecret, ClaimCode: claimCode}, nil
}
