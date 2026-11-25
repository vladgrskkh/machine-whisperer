package telemetry

import (
	"context"
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/vladgrskkh/machine-whisperer/api/internal/domain/machine"
	"github.com/vladgrskkh/machine-whisperer/api/internal/domain/telemetry"
	"github.com/vladgrskkh/machine-whisperer/api/internal/handler"
	"github.com/vladgrskkh/machine-whisperer/api/internal/handler/middleware"
	telemetryusecase "github.com/vladgrskkh/machine-whisperer/api/internal/usecase/telemetry"
)

type Ingester interface {
	Ingest(
		ctx context.Context,
		deviceID, assignedMachineID, requestedMachineID string,
		observation telemetry.Observation,
	) error
}

// NewIngest handles machine-scoped ingestion for legacy device clients.
//
// @Summary Submit a telemetry observation by machine ID
// @ID ingestMachineTelemetry
// @Tags telemetry
// @Description Use POST /v1/devices/me/telemetry for new integrations. The path UUID must match the claimed device's assigned machine.
// @Description The API assigns the telemetry_writer capability to the authenticated device key.
// @Description Send new observations in nondecreasing timestamp order for the machine. Reuse an event ID with the same values on retry.
// @Description The API normalizes observed_at to UTC microseconds. It accepts observations at most five minutes ahead of database time and strictly less than 30 days old.
// @Description Accepted observations appear in history after export.
// @Description Send one JSON object of at most 8 KiB. Malformed JSON, unknown or duplicate keys and trailing values return 400.
// @Accept json
// @Produce json
// @Security DeviceBearer
// @Param machine_id path string true "Machine UUID." Format(uuid)
// @Param body body Observation true "Telemetry observation."
// @Success 202 "Observation durably queued for export or an exact accepted retry."
// @Failure 400 {object} ErrorResponse "Invalid machine ID or observation (`invalid_input`)."
// @Failure 401 {object} ErrorResponse "Invalid or missing bearer credentials (`authentication_failed`)."
// @Failure 404 {object} ErrorResponse "The device is unclaimed or revoked, or the machine is unavailable to it (`machine_not_found`)."
// @Failure 409 {object} ErrorResponse "The event ID was reused with different values, or the observation is out of order or outside its acceptance window (`telemetry_conflict`)."
// @Failure 500 {object} ErrorResponse "Unexpected server error (`internal_error`)."
// @Header all {string} Cache-Control "Always no-store."
// @Header 401 {string} WWW-Authenticate "Bearer"
// @Deprecated
// @Router /v1/machines/{machine_id}/telemetry [post]
func NewIngest(usecase Ingester) http.Handler {
	return newIngest(usecase, true)
}

// NewDeviceIngest handles ingestion using the device's machine binding.
//
// @Summary Submit a device telemetry observation
// @ID ingestDeviceTelemetry
// @Tags telemetry
// @Description The API derives the machine from the claimed device's binding. Send only observation fields in the body.
// @Description The API assigns the telemetry_writer capability to the authenticated device key.
// @Description Send new observations in nondecreasing timestamp order for the machine. Reuse an event ID with the same values on retry.
// @Description The API normalizes observed_at to UTC microseconds. It accepts observations at most five minutes ahead of database time and strictly less than 30 days old.
// @Description Accepted observations appear in history after export.
// @Description Send one JSON object of at most 8 KiB. Malformed JSON, unknown or duplicate keys and trailing values return 400.
// @Accept json
// @Produce json
// @Security DeviceBearer
// @Param body body Observation true "Telemetry observation."
// @Success 202 "Observation durably queued for export or an exact accepted retry."
// @Failure 400 {object} ErrorResponse "Invalid telemetry observation (`invalid_input`)."
// @Failure 401 {object} ErrorResponse "Invalid or missing bearer credentials (`authentication_failed`)."
// @Failure 404 {object} ErrorResponse "The device is unclaimed or revoked, or the machine is unavailable to it (`machine_not_found`)."
// @Failure 409 {object} ErrorResponse "The event ID was reused with different values, or the observation is out of order or outside its acceptance window (`telemetry_conflict`)."
// @Failure 500 {object} ErrorResponse "Unexpected server error (`internal_error`)."
// @Header all {string} Cache-Control "Always no-store."
// @Header 401 {string} WWW-Authenticate "Bearer"
// @Router /v1/devices/me/telemetry [post]
func NewDeviceIngest(usecase Ingester) http.Handler {
	return newIngest(usecase, false)
}

func newIngest(usecase Ingester, machineIDFromPath bool) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		deviceID, assignedMachineID, ok := middleware.DeviceBinding(r.Context())
		if !ok {
			middleware.Unauthorized(w)
			return
		}
		requestedMachineID := assignedMachineID
		if machineIDFromPath {
			parsedMachineID, err := handler.ParseUUID(chi.URLParam(r, "machine_id"))
			if err != nil {
				handler.Error(w, http.StatusBadRequest, "invalid_input", "Invalid machine ID")
				return
			}
			requestedMachineID = parsedMachineID
		}
		observation, err := decodeObservation(r.Body)
		if err == nil {
			err = usecase.Ingest(r.Context(), deviceID, assignedMachineID, requestedMachineID, observation)
		}
		switch {
		case errors.Is(err, handler.ErrInvalidBody), errors.Is(err, telemetryusecase.ErrInvalidObservation):
			handler.Error(w, http.StatusBadRequest, "invalid_input", "Invalid telemetry observation")
		case errors.Is(err, machine.ErrNotFound):
			handler.Error(w, http.StatusNotFound, "machine_not_found", "Machine not found")
		case errors.Is(err, telemetry.ErrEventConflict),
			errors.Is(err, telemetryusecase.ErrObservationWindow),
			errors.Is(err, telemetryusecase.ErrObservationOrder):
			handler.Error(
				w,
				http.StatusConflict,
				"telemetry_conflict",
				"Telemetry observation conflicts with acceptance rules",
			)
		case err != nil:
			handler.InternalError(w)
		default:
			w.WriteHeader(http.StatusAccepted)
		}
	})
}

func decodeObservation(body io.Reader) (telemetry.Observation, error) {
	var value telemetry.Observation
	var observedAt string
	var vibrationRMS *float64
	err := handler.DecodeObjectWithOptional(body, handler.MaxJSONBody, map[string]any{
		"schema_version":  &value.SchemaVersion,
		"event_id":        &value.EventID,
		"observed_at":     &observedAt,
		"operating_state": &value.OperatingState,
		"vibration_rms_g": &vibrationRMS,
	}, map[string]any{
		"dominant_frequency_hz": &value.DominantFrequencyHz,
		"current_rms_a":         &value.CurrentRMSA,
	})
	if err != nil || vibrationRMS == nil {
		return telemetry.Observation{}, handler.ErrInvalidBody
	}
	value.ObservedAt, err = time.Parse(time.RFC3339, observedAt)
	if err != nil {
		return telemetry.Observation{}, handler.ErrInvalidBody
	}
	value.VibrationRMSG = *vibrationRMS
	return value, nil
}
