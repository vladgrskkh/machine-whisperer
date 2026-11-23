package devices

import (
	"context"
	"errors"
	"net/http"

	"github.com/vladgrskkh/machine-whisperer/api/internal/domain/device"
	"github.com/vladgrskkh/machine-whisperer/api/internal/handler"
	"github.com/vladgrskkh/machine-whisperer/api/internal/handler/middleware"
)

type Claimer interface {
	Claim(ctx context.Context, deviceID, machineID, ownerUserID string, code device.Secret) error
}

type claimRequest struct {
	deviceID  string
	machineID string
	code      device.Secret
}

type claimResponse struct {
	DeviceID  string `json:"device_id"`
	MachineID string `json:"machine_id"`
	Status    string `json:"status"`
}

func NewClaim(usecase Claimer) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		owner, ok := middleware.UserID(r.Context())
		if !ok {
			middleware.Unauthorized(w)
			return
		}
		request, err := decodeClaim(r)
		if err != nil {
			handler.Error(w, http.StatusBadRequest, "invalid_input", "Invalid device claim or request body")
			return
		}
		err = usecase.Claim(r.Context(), request.deviceID, request.machineID, owner, request.code)
		switch {
		case errors.Is(err, device.ErrClaimUnavailable):
			handler.Error(w, http.StatusNotFound, "claim_unavailable", "Device claim unavailable")
		case err != nil:
			handler.InternalError(w)
		default:
			handler.JSON(w, http.StatusOK, claimResponse{
				DeviceID: request.deviceID, MachineID: request.machineID, Status: "claimed",
			})
		}
	})
}

func decodeClaim(r *http.Request) (claimRequest, error) {
	var deviceID, machineID, claimCode string
	if err := handler.DecodeObject(r.Body, handler.MaxJSONBody, map[string]any{
		"device_id": &deviceID, "machine_id": &machineID, "claim_code": &claimCode,
	}); err != nil {
		return claimRequest{}, err
	}
	canonicalDevice, err := handler.ParseUUID(deviceID)
	if err != nil {
		return claimRequest{}, err
	}
	canonicalMachine, err := handler.ParseUUID(machineID)
	if err != nil {
		return claimRequest{}, err
	}
	code, err := device.ParseSecret(claimCode)
	if err != nil {
		return claimRequest{}, err
	}
	return claimRequest{deviceID: canonicalDevice, machineID: canonicalMachine, code: code}, nil
}
