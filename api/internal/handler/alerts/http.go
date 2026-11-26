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
	ID                     string    `json:"id"                       validate:"required" format:"uuid"`
	MachineID              string    `json:"machine_id"               validate:"required" format:"uuid"`
	TriggerDeviceID        string    `json:"trigger_device_id"        validate:"required" format:"uuid"`
	TriggerEventID         string    `json:"trigger_event_id"         validate:"required" format:"uuid"`
	TriggeredAt            time.Time `json:"triggered_at"             validate:"required" format:"date-time"`
	TriggerZScore          float64   `json:"trigger_z_score"          validate:"required" format:"double"    minimum:"0"`
	TriggerBaselineVersion int64     `json:"trigger_baseline_version" validate:"required" format:"int64"     minimum:"1"`
	// Baseline RMS vibration mean at the trigger, in g.
	TriggerMeanRMSG float64 `json:"trigger_mean_rms_g" validate:"required" format:"double" minimum:"0"`
	// Baseline RMS vibration standard deviation at the trigger, in g. Strictly positive.
	TriggerStddevRMSG       float64    `json:"trigger_stddev_rms_g"      validate:"required" format:"double"    minimum:"0"`
	LastZScore              float64    `json:"last_z_score"              validate:"required" format:"double"    minimum:"0"`
	PeakZScore              float64    `json:"peak_z_score"              validate:"required" format:"double"    minimum:"0"`
	RecoveryDeviceID        *string    `json:"recovery_device_id"        validate:"required" format:"uuid"                  extensions:"x-nullable"`
	RecoveryEventID         *string    `json:"recovery_event_id"         validate:"required" format:"uuid"                  extensions:"x-nullable"`
	RecoveredAt             *time.Time `json:"recovered_at"              validate:"required" format:"date-time"             extensions:"x-nullable"`
	RecoveryBaselineVersion *int64     `json:"recovery_baseline_version" validate:"required" format:"int64"     minimum:"1" extensions:"x-nullable"`
	AcknowledgedAt          *time.Time `json:"acknowledged_at"           validate:"required" format:"date-time"             extensions:"x-nullable"`
} // @name Alert

// NewList handles requests to list alerts for one machine.
//
// @Summary List machine alerts
// @ID listMachineAlerts
// @Tags alerts
// @Description The machine must be active and belong to the caller. Orders alerts by newest trigger first and returns an empty array when none exist.
// @Produce json
// @Security UserAccessJWT
// @Param id path string true "Machine UUID." Format(uuid)
// @Success 200 {array} Alert "Machine alerts."
// @Failure 400 {object} ErrorResponse "Invalid machine ID (`invalid_input`)."
// @Failure 401 {object} ErrorResponse "Invalid or missing bearer credentials (`authentication_failed`)."
// @Failure 404 {object} ErrorResponse "The machine is unavailable to the caller (`machine_not_found`)."
// @Failure 500 {object} ErrorResponse "Unexpected server error (`internal_error`)."
// @Header all {string} Cache-Control "Always no-store."
// @Header 401 {string} WWW-Authenticate "Bearer"
// @Router /v1/machines/{id}/alerts [get]
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

// NewAcknowledge handles acknowledgement of a machine alert.
//
// @Summary Acknowledge an alert
// @ID acknowledgeAlert
// @Tags alerts
// @Description The alert must belong to an active machine owned by the caller. Repeated acknowledgements preserve the first timestamp and leave the recovery state unchanged.
// @Produce json
// @Security UserAccessJWT
// @Param id path string true "Alert UUID." Format(uuid)
// @Success 204 "Alert acknowledged."
// @Failure 400 {object} ErrorResponse "Invalid alert ID (`invalid_input`)."
// @Failure 401 {object} ErrorResponse "Invalid or missing bearer credentials (`authentication_failed`)."
// @Failure 404 {object} ErrorResponse "The alert is unavailable to the caller (`alert_not_found`)."
// @Failure 500 {object} ErrorResponse "Unexpected server error (`internal_error`)."
// @Header all {string} Cache-Control "Always no-store."
// @Header 401 {string} WWW-Authenticate "Bearer"
// @Router /v1/alerts/{id}/acknowledge [post]
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
