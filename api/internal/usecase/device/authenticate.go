package device

import (
	"context"
	"crypto/subtle"

	"go.opentelemetry.io/otel/trace"

	"github.com/vladgrskkh/machine-whisperer/api/internal/domain/device"
	"github.com/vladgrskkh/machine-whisperer/api/internal/spanutil"
)

type AuthenticationRepository interface {
	FindForAuthentication(ctx context.Context, deviceID string) (device.Device, error)
}

type Authenticator struct {
	tracer     trace.Tracer
	repository AuthenticationRepository
}

func NewAuthenticator(repository AuthenticationRepository, tracer trace.Tracer) *Authenticator {
	return &Authenticator{tracer: tracer, repository: repository}
}

//nolint:nonamedreturns // Span completion observes the returned error.
func (a *Authenticator) Authenticate(ctx context.Context, deviceID string, secret device.Secret) (_ string, err error) {
	ctx, span := a.tracer.Start(ctx, "device.authenticate")
	defer func() { spanutil.End(span, err, device.ErrInvalidCredentials, device.ErrUnavailable) }()
	value, err := a.repository.FindForAuthentication(ctx, deviceID)
	if err != nil {
		return "", err
	}
	verifier := secret.Verifier()
	if subtle.ConstantTimeCompare(verifier[:], value.KeyVerifier[:]) != 1 {
		return "", device.ErrInvalidCredentials
	}
	if value.MachineID == nil || value.RevokedAt != nil {
		return "", device.ErrUnavailable
	}
	return *value.MachineID, nil
}
