package devices

import (
	"context"
	"log/slog"
	"net/http"

	"github.com/vladgrskkh/machine-whisperer/api/internal/handler"
	"github.com/vladgrskkh/machine-whisperer/api/internal/usecase/device"
)

type Provisioner interface {
	Provision(context.Context) (device.Provisioned, error)
}

type provisionResponse struct {
	DeviceID     string       `json:"device_id"`
	APIKey       string       `json:"api_key"`
	ClaimPayload ClaimPayload `json:"claim_payload"`
}

func NewProvision(usecase Provisioner) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		result, err := usecase.Provision(r.Context())
		if err != nil {
			slog.ErrorContext(r.Context(), "device provisioning failed", "error", err)
			handler.InternalError(w)
			return
		}
		handler.JSON(w, http.StatusCreated, provisionResponse{
			DeviceID: result.DeviceID,
			APIKey:   result.DeviceID + "." + result.APISecret.Encode(),
			ClaimPayload: ClaimPayload{
				Version:   claimPayloadVersion,
				DeviceID:  result.DeviceID,
				ClaimCode: result.ClaimCode.Encode(),
			},
		})
	})
}
