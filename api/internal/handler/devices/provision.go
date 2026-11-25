package devices

import (
	"context"
	"net/http"

	"github.com/vladgrskkh/machine-whisperer/api/internal/handler"
	"github.com/vladgrskkh/machine-whisperer/api/internal/handler/middleware"
	"github.com/vladgrskkh/machine-whisperer/api/internal/usecase/device"
)

type Provisioner interface {
	Provision(context.Context, string) (device.Provisioned, error)
}

type provisionResponse struct {
	DeviceID string `json:"device_id" validate:"required" format:"uuid"`
	// Issued once as <device_id>.<secret>. The secret is 32 bytes encoded as unpadded base64url.
	APIKey       string       `json:"api_key"       validate:"required"`
	ClaimPayload ClaimPayload `json:"claim_payload" validate:"required"`
} // @name ProvisionedDevice

// NewProvision handles provisioning requests from manufacturing.
//
// @Summary Provision a device
// @ID provisionDevice
// @Tags internal
// @Description Requires the device_provisioner role. Deployment ingress restricts access to the configured manufacturing CIDR.
// @Description The API key is issued once. The separate claim payload is printed on the unit's QR label.
// @Description Shares a per-IP request budget with signup, login, refresh, device provisioning and device claim.
// @Produce json
// @Security UserAccessJWT
// @Success 201 {object} ProvisionedDevice "Device credentials and QR claim payload."
// @Failure 401 {object} ErrorResponse "Invalid or missing bearer credentials (`authentication_failed`)."
// @Failure 403 {object} ErrorResponse "The user lacks the device_provisioner role (`forbidden`)."
// @Failure 429 {object} ErrorResponse "The shared per-IP request budget was exceeded (`rate_limited`)."
// @Failure 500 {object} ErrorResponse "Unexpected server error (`internal_error`)."
// @Header all {string} Cache-Control "Always no-store."
// @Header 401 {string} WWW-Authenticate "Bearer"
// @Header 429 {integer} Retry-After "Seconds until another attempt can be made."
// @Router /internal/v1/device-provisioning [post]
func NewProvision(usecase Provisioner) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		provisionerID, ok := middleware.UserID(r.Context())
		if !ok {
			middleware.Unauthorized(w)
			return
		}
		result, err := usecase.Provision(r.Context(), provisionerID)
		if err != nil {
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
