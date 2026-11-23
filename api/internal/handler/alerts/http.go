package alerts

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/vladgrskkh/machine-whisperer/api/internal/domain/alert"
	"github.com/vladgrskkh/machine-whisperer/api/internal/domain/machine"
	"github.com/vladgrskkh/machine-whisperer/api/internal/handler"
	"github.com/vladgrskkh/machine-whisperer/api/internal/handler/middleware"
)

type Lister interface {
	Execute(ctx context.Context, ownerID, machineID string) ([]alert.Alert, error)
}

type Acknowledger interface {
	Execute(ctx context.Context, ownerID, alertID string) error
}

type alertResponse struct {
	ID                      string     `json:"id"`
	MachineID               string     `json:"machine_id"`
	TriggerDeviceID         string     `json:"trigger_device_id"`
	TriggerEventID          string     `json:"trigger_event_id"`
	TriggeredAt             time.Time  `json:"triggered_at"`
	TriggerZScore           float64    `json:"trigger_z_score"`
	TriggerBaselineVersion  int64      `json:"trigger_baseline_version"`
	TriggerMeanRMSG         float64    `json:"trigger_mean_rms_g"`
	TriggerStddevRMSG       float64    `json:"trigger_stddev_rms_g"`
	LastZScore              float64    `json:"last_z_score"`
	PeakZScore              float64    `json:"peak_z_score"`
	RecoveryDeviceID        *string    `json:"recovery_device_id"`
	RecoveryEventID         *string    `json:"recovery_event_id"`
	RecoveredAt             *time.Time `json:"recovered_at"`
	RecoveryBaselineVersion *int64     `json:"recovery_baseline_version"`
	AcknowledgedAt          *time.Time `json:"acknowledged_at"`
}

func NewList(usecase Lister) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ownerID, machineID, ok := ownerTarget(w, r, "Invalid machine ID")
		if !ok {
			return
		}
		values, err := usecase.Execute(r.Context(), ownerID, machineID)
		switch {
		case errors.Is(err, machine.ErrNotFound):
			handler.Error(w, http.StatusNotFound, "machine_not_found", "Machine not found")
		case err != nil:
			handler.InternalError(w)
		default:
			result := make([]alertResponse, 0, len(values))
			for _, value := range values {
				result = append(result, alertResponse(value))
			}
			handler.JSON(w, http.StatusOK, result)
		}
	})
}

func NewAcknowledge(usecase Acknowledger) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ownerID, alertID, ok := ownerTarget(w, r, "Invalid alert ID")
		if !ok {
			return
		}
		err := usecase.Execute(r.Context(), ownerID, alertID)
		switch {
		case errors.Is(err, alert.ErrNotFound):
			handler.Error(w, http.StatusNotFound, "alert_not_found", "Alert not found")
		case err != nil:
			handler.InternalError(w)
		default:
			w.WriteHeader(http.StatusNoContent)
		}
	})
}

func ownerTarget(w http.ResponseWriter, r *http.Request, invalidMessage string) (string, string, bool) {
	ownerID, ok := middleware.UserID(r.Context())
	if !ok {
		middleware.Unauthorized(w)
		return "", "", false
	}
	id, err := handler.ParseUUID(chi.URLParam(r, "id"))
	if err != nil {
		handler.Error(w, http.StatusBadRequest, "invalid_input", invalidMessage)
		return "", "", false
	}
	return ownerID, id, true
}
