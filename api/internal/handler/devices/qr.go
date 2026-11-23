package devices

import (
	"errors"
	"io"

	"github.com/vladgrskkh/machine-whisperer/api/internal/domain/device"
	"github.com/vladgrskkh/machine-whisperer/api/internal/handler"
)

const (
	claimPayloadVersion = 1
	maxQRPayload        = 1024
)

var ErrInvalidClaimPayload = errors.New("invalid QR claim payload")

type ClaimPayload struct {
	Version   int    `json:"v"`
	DeviceID  string `json:"device_id"`
	ClaimCode string `json:"claim_code"`
}

func DecodeClaimPayload(reader io.Reader) (ClaimPayload, error) {
	var payload ClaimPayload
	if err := handler.DecodeObject(reader, maxQRPayload, map[string]any{
		"v": &payload.Version, "device_id": &payload.DeviceID, "claim_code": &payload.ClaimCode,
	}); err != nil || payload.Version != claimPayloadVersion {
		return ClaimPayload{}, ErrInvalidClaimPayload
	}
	id, err := handler.ParseUUID(payload.DeviceID)
	if err != nil {
		return ClaimPayload{}, ErrInvalidClaimPayload
	}
	if _, err = device.ParseSecret(payload.ClaimCode); err != nil {
		return ClaimPayload{}, ErrInvalidClaimPayload
	}
	payload.DeviceID = id
	return payload, nil
}
