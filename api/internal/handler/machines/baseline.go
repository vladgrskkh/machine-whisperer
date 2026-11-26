package machines

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/vladgrskkh/machine-whisperer/api/internal/domain/machine"
	"github.com/vladgrskkh/machine-whisperer/api/internal/handler"
)

const invalidBaseline = "Invalid baseline values or request body"

type BaselineGetter interface {
	Execute(ctx context.Context, ownerID, machineID string) (machine.Baseline, error)
}

type BaselineSetter interface {
	Execute(ctx context.Context, ownerID, machineID string, meanRMSG, stddevRMSG float64) (machine.Baseline, error)
}

type baselineResponse struct {
	MachineID string `json:"machine_id" validate:"required" format:"uuid"`
	// Mean RMS vibration in g.
	MeanRMSG float64 `json:"mean_rms_g" validate:"required" format:"double" minimum:"0"`
	// Standard deviation of RMS vibration in g. Strictly positive.
	StddevRMSG float64   `json:"stddev_rms_g" validate:"required" format:"double"    minimum:"0"`
	Version    int64     `json:"version"      validate:"required" format:"int64"     minimum:"1"`
	UpdatedAt  time.Time `json:"updated_at"   validate:"required" format:"date-time"`
} // @name Baseline

//nolint:unused // Documents the JSON fields read by the baseline update handler.
type baselineBody struct {
	// Mean RMS vibration in g. Must be finite and nonnegative.
	MeanRMSG float64 `json:"mean_rms_g" validate:"required" format:"double" minimum:"0"`
	// Standard deviation of RMS vibration in g. Must be finite and strictly positive.
	StddevRMSG float64 `json:"stddev_rms_g" validate:"required" format:"double" minimum:"0"`
} // @name BaselineInput

// NewGetBaseline handles reads of a machine's current baseline.
//
// @Summary Get the current vibration baseline
// @ID getMachineBaseline
// @Tags machines
// @Description The machine must be active and belong to the caller.
// @Produce json
// @Security UserAccessJWT
// @Param id path string true "Machine UUID." Format(uuid)
// @Success 200 {object} Baseline "Current baseline."
// @Failure 400 {object} ErrorResponse "Invalid machine ID (`invalid_input`)."
// @Failure 401 {object} ErrorResponse "Invalid or missing bearer credentials (`authentication_failed`)."
// @Failure 404 {object} ErrorResponse "No baseline is available for this machine and owner (`baseline_not_found`)."
// @Failure 500 {object} ErrorResponse "Unexpected server error (`internal_error`)."
// @Header all {string} Cache-Control "Always no-store."
// @Header 401 {string} WWW-Authenticate "Bearer"
// @Router /v1/machines/{id}/baseline [get]
func NewGetBaseline(usecase BaselineGetter) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ownerID, machineID, ok := machineTarget(w, r)
		if !ok {
			return
		}
		baseline, err := usecase.Execute(r.Context(), ownerID, machineID)
		writeBaseline(w, baseline, err)
	})
}

// NewSetBaseline handles writes to a machine's current baseline.
//
// @Summary Set the current vibration baseline
// @ID setMachineBaseline
// @Tags machines
// @Description The machine must be active and belong to the caller. Repeating the same values preserves the version and timestamp.
// @Description Send one JSON object of at most 8 KiB. Malformed JSON, unknown or duplicate keys and trailing values return 400.
// @Accept json
// @Produce json
// @Security UserAccessJWT
// @Param id path string true "Machine UUID." Format(uuid)
// @Param body body BaselineInput true "RMS vibration mean and standard deviation in g."
// @Success 200 {object} Baseline "Stored baseline."
// @Failure 400 {object} ErrorResponse "Invalid machine ID, baseline values or request body (`invalid_input`)."
// @Failure 401 {object} ErrorResponse "Invalid or missing bearer credentials (`authentication_failed`)."
// @Failure 404 {object} ErrorResponse "The machine is unavailable to the caller (`machine_not_found`)."
// @Failure 500 {object} ErrorResponse "Unexpected server error (`internal_error`)."
// @Header all {string} Cache-Control "Always no-store."
// @Header 401 {string} WWW-Authenticate "Bearer"
// @Router /v1/machines/{id}/baseline [put]
func NewSetBaseline(usecase BaselineSetter) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ownerID, machineID, ok := machineTarget(w, r)
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
