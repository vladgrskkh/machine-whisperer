package middleware

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/vladgrskkh/machine-whisperer/api/internal/domain/device"
	"github.com/vladgrskkh/machine-whisperer/api/internal/handler"
)

type DeviceAuthenticator interface {
	Authenticate(ctx context.Context, deviceID string, secret device.Secret) (string, error)
}

type deviceIdentityKey struct{}

type deviceIdentity struct {
	deviceID  string
	machineID string
}

func DeviceBinding(ctx context.Context) (string, string, bool) {
	value, ok := ctx.Value(deviceIdentityKey{}).(deviceIdentity)
	return value.deviceID, value.machineID, ok
}

func DeviceToken(authenticator DeviceAuthenticator) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			deviceID, secret, err := parseDeviceToken(r.Header.Values("Authorization"))
			if err != nil {
				Unauthorized(w)
				return
			}
			ctx, cancel := context.WithTimeout(r.Context(), accessTimeout)
			defer cancel()
			machineID, err := authenticator.Authenticate(ctx, deviceID, secret)
			switch {
			case errors.Is(err, device.ErrInvalidCredentials):
				Unauthorized(w)
			case errors.Is(err, device.ErrUnavailable):
				handler.Error(w, http.StatusNotFound, "machine_not_found", "Machine not found")
			case err != nil:
				handler.InternalError(w)
			default:
				value := deviceIdentity{deviceID: deviceID, machineID: machineID}
				next.ServeHTTP(w, r.WithContext(context.WithValue(ctx, deviceIdentityKey{}, value)))
			}
		})
	}
}

func parseDeviceToken(values []string) (string, device.Secret, error) {
	if len(values) != 1 {
		return "", device.Secret{}, device.ErrInvalidCredentials
	}
	scheme, key, ok := strings.Cut(values[0], " ")
	if !ok || !strings.EqualFold(scheme, "Bearer") {
		return "", device.Secret{}, device.ErrInvalidCredentials
	}
	id, encoded, ok := strings.Cut(key, ".")
	if !ok {
		return "", device.Secret{}, device.ErrInvalidCredentials
	}
	deviceID, err := handler.ParseUUID(id)
	if err != nil {
		return "", device.Secret{}, device.ErrInvalidCredentials
	}
	secret, err := device.ParseSecret(encoded)
	if err != nil {
		return "", device.Secret{}, device.ErrInvalidCredentials
	}
	return deviceID, secret, nil
}
