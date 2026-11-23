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

func NewIngest(usecase Ingester) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		deviceID, assignedMachineID, ok := middleware.DeviceBinding(r.Context())
		if !ok {
			middleware.Unauthorized(w)
			return
		}
		requestedMachineID, err := handler.ParseUUID(chi.URLParam(r, "machine_id"))
		if err != nil {
			handler.Error(w, http.StatusBadRequest, "invalid_input", "Invalid machine ID")
			return
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
	var observedAt *time.Time
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
	if err != nil || observedAt == nil || vibrationRMS == nil {
		return telemetry.Observation{}, handler.ErrInvalidBody
	}
	value.ObservedAt = *observedAt
	value.VibrationRMSG = *vibrationRMS
	return value, nil
}
