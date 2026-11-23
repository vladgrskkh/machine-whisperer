package machines

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/vladgrskkh/machine-whisperer/api/internal/domain/machine"
	"github.com/vladgrskkh/machine-whisperer/api/internal/handler"
	"github.com/vladgrskkh/machine-whisperer/api/internal/handler/middleware"
)

const invalidBaseline = "Invalid baseline values or request body"

type BaselineGetter interface {
	Execute(ctx context.Context, ownerID, machineID string) (machine.Baseline, error)
}

type BaselineSetter interface {
	Execute(ctx context.Context, ownerID, machineID string, meanRMSG, stddevRMSG float64) (machine.Baseline, error)
}

type baselineResponse struct {
	MachineID  string    `json:"machine_id"`
	MeanRMSG   float64   `json:"mean_rms_g"`
	StddevRMSG float64   `json:"stddev_rms_g"`
	Version    int64     `json:"version"`
	UpdatedAt  time.Time `json:"updated_at"`
}

func NewGetBaseline(usecase BaselineGetter) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ownerID, machineID, ok := baselineTarget(w, r)
		if !ok {
			return
		}
		baseline, err := usecase.Execute(r.Context(), ownerID, machineID)
		writeBaseline(w, baseline, err)
	})
}

func NewSetBaseline(usecase BaselineSetter) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ownerID, machineID, ok := baselineTarget(w, r)
		if !ok {
			return
		}

		var meanRMSG, stddevRMSG *float64
		fields := map[string]any{"mean_rms_g": &meanRMSG, "stddev_rms_g": &stddevRMSG}
		if err := handler.DecodeObject(r.Body, handler.MaxJSONBody, fields); err != nil ||
			meanRMSG == nil || stddevRMSG == nil {
			handler.Error(w, http.StatusBadRequest, invalidInput, invalidBaseline)
			return
		}

		baseline, err := usecase.Execute(r.Context(), ownerID, machineID, *meanRMSG, *stddevRMSG)
		writeBaseline(w, baseline, err)
	})
}

func baselineTarget(w http.ResponseWriter, r *http.Request) (string, string, bool) {
	ownerID, ok := middleware.UserID(r.Context())
	if !ok {
		middleware.Unauthorized(w)
		return "", "", false
	}
	machineID, err := handler.ParseUUID(chi.URLParam(r, "id"))
	if err != nil {
		handler.Error(w, http.StatusBadRequest, invalidInput, "Invalid machine ID")
		return "", "", false
	}
	return ownerID, machineID, true
}

func writeBaseline(w http.ResponseWriter, baseline machine.Baseline, err error) {
	switch {
	case errors.Is(err, machine.ErrInvalidBaseline):
		handler.Error(w, http.StatusBadRequest, invalidInput, invalidBaseline)
	case errors.Is(err, machine.ErrBaselineNotFound):
		handler.Error(w, http.StatusNotFound, "baseline_not_found", "Baseline not found")
	case errors.Is(err, machine.ErrNotFound):
		handler.Error(w, http.StatusNotFound, "machine_not_found", "Machine not found")
	case err != nil:
		handler.InternalError(w)
	default:
		handler.JSON(w, http.StatusOK, baselineResponse{
			MachineID:  baseline.MachineID,
			MeanRMSG:   baseline.MeanRMSG,
			StddevRMSG: baseline.StddevRMSG,
			Version:    baseline.Version,
			UpdatedAt:  baseline.UpdatedAt,
		})
	}
}
