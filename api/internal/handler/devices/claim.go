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
	DeviceID  string `json:"device_id"  validate:"required" format:"uuid"`
	MachineID string `json:"machine_id" validate:"required" format:"uuid"`
	Status    string `json:"status"     validate:"required"               enums:"claimed"`
} // @name ClaimedDevice

//nolint:unused // Documents the JSON fields read by decodeClaim.
type deviceClaimBody struct {
	DeviceID  string `json:"device_id"  validate:"required" format:"uuid"`
	MachineID string `json:"machine_id" validate:"required" format:"uuid"`
	// Single-use claim code from the QR payload, a canonical unpadded base64url encoding of 32 bytes.
	ClaimCode string `json:"claim_code" validate:"required" minLength:"43" maxLength:"43"`
} // @name DeviceClaim

// NewClaim handles a user's device claim.
//
// @Summary Claim a device for a machine
// @ID claimDevice
// @Tags devices
// @Description Binds the device to a machine owned by the caller and consumes the single-use claim code.
// @Description Send one JSON object of at most 8 KiB. Malformed JSON, unknown or duplicate keys and trailing values return 400.
// @Description Shares a per-IP request budget with signup, login, refresh, device provisioning and device claim.
// @Accept json
// @Produce json
// @Security UserAccessJWT
// @Param body body DeviceClaim true "QR claim credentials and selected machine ID."
// @Success 200 {object} ClaimedDevice "Device claimed."
// @Failure 400 {object} ErrorResponse "Invalid device ID, machine ID, claim code or request body (`invalid_input`)."
// @Failure 401 {object} ErrorResponse "Invalid or missing bearer credentials (`authentication_failed`)."
// @Failure 404 {object} ErrorResponse "The device, claim code or machine is unavailable (`claim_unavailable`)."
// @Failure 429 {object} ErrorResponse "The shared per-IP request budget was exceeded (`rate_limited`)."
// @Failure 500 {object} ErrorResponse "Unexpected server error (`internal_error`)."
// @Header all {string} Cache-Control "Always no-store."
// @Header 401 {string} WWW-Authenticate "Bearer"
// @Header 429 {integer} Retry-After "Seconds until another attempt can be made."
// @Router /v1/devices/claim [post]
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
