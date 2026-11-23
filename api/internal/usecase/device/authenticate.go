package device

import (
	"context"
	"crypto/subtle"

	"github.com/vladgrskkh/machine-whisperer/api/internal/domain/device"
)

type AuthenticationRepository interface {
	FindForAuthentication(ctx context.Context, deviceID string) (device.Device, error)
}

type Authenticator struct {
	repository AuthenticationRepository
}

func NewAuthenticator(repository AuthenticationRepository) *Authenticator {
	return &Authenticator{repository: repository}
}

func (a *Authenticator) Authenticate(ctx context.Context, deviceID string, secret device.Secret) (string, error) {
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
