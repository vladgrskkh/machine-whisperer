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

const invalidInput = "invalid_input"

type Creator interface {
	Create(ctx context.Context, ownerUserID, name string) (machine.Machine, error)
}

type Lister interface {
	List(context.Context, string) ([]machine.Machine, error)
}

type Detailer interface {
	Detail(ctx context.Context, ownerUserID, machineID string) (machine.Machine, error)
}

type machineResponse struct {
	ID        string    `json:"id"         validate:"required" format:"uuid"`
	Name      string    `json:"name"       validate:"required"                    minLength:"1" maxLength:"100"`
	CreatedAt time.Time `json:"created_at" validate:"required" format:"date-time"`
} // @name Machine

//nolint:unused // Documents the JSON field read by the machine creation handler.
type machineNameBody struct {
	// After trimming, the name must contain 1–100 Unicode code points and no control characters.
	Name string `json:"name" validate:"required" minLength:"1"`
} // @name MachineName

// NewCreate handles machine creation for the authenticated owner.
//
// @Summary Create a machine
// @ID createMachine
// @Tags machines
// @Description Creates a machine owned by the authenticated user.
// @Description Send one JSON object of at most 8 KiB. Malformed JSON, unknown or duplicate keys and trailing values return 400.
// @Accept json
// @Produce json
// @Security UserAccessJWT
// @Param body body MachineName true "Machine name."
// @Success 201 {object} Machine "Machine created."
// @Failure 400 {object} ErrorResponse "Invalid machine name or request body (`invalid_input`)."
// @Failure 401 {object} ErrorResponse "Invalid or missing bearer credentials (`authentication_failed`)."
// @Failure 500 {object} ErrorResponse "Unexpected server error (`internal_error`)."
// @Header all {string} Cache-Control "Always no-store."
// @Header 401 {string} WWW-Authenticate "Bearer"
// @Router /v1/machines [post]
func NewCreate(usecase Creator) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		owner, ok := middleware.UserID(r.Context())
		if !ok {
			middleware.Unauthorized(w)
			return
		}
		var name string
		if err := handler.DecodeObject(r.Body, handler.MaxJSONBody, map[string]any{"name": &name}); err != nil {
			handler.Error(w, http.StatusBadRequest, invalidInput, "Invalid machine name or request body")
			return
		}
		value, err := usecase.Create(r.Context(), owner, name)
		switch {
		case errors.Is(err, machine.ErrInvalidName):
			handler.Error(w, http.StatusBadRequest, invalidInput, "Invalid machine name or request body")
		case errors.Is(err, machine.ErrNotFound):
			middleware.Unauthorized(w)
		case err != nil:
			handler.InternalError(w)
		default:
			handler.JSON(w, http.StatusCreated, publicMachine(value))
		}
	})
}

// NewList handles requests to list the current user's machines.
//
// @Summary List the user's machines
// @ID listMachines
// @Tags machines
// @Description Returns an empty array when the user owns no machines.
// @Produce json
// @Security UserAccessJWT
// @Success 200 {array} Machine "Owned machines."
// @Failure 401 {object} ErrorResponse "Invalid or missing bearer credentials (`authentication_failed`)."
// @Failure 500 {object} ErrorResponse "Unexpected server error (`internal_error`)."
// @Header all {string} Cache-Control "Always no-store."
// @Header 401 {string} WWW-Authenticate "Bearer"
// @Router /v1/machines [get]
func NewList(usecase Lister) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		owner, ok := middleware.UserID(r.Context())
		if !ok {
			middleware.Unauthorized(w)
			return
		}
		values, err := usecase.List(r.Context(), owner)
		if err != nil {
			handler.InternalError(w)
			return
		}
		result := make([]machineResponse, 0, len(values))
		for _, value := range values {
			result = append(result, publicMachine(value))
		}
		handler.JSON(w, http.StatusOK, result)
	})
}

// NewDetail handles a request for one machine.
//
// @Summary Get a machine
// @ID getMachine
// @Tags machines
// @Description The machine must belong to the caller.
// @Produce json
// @Security UserAccessJWT
// @Param id path string true "Machine UUID." Format(uuid)
// @Success 200 {object} Machine "Machine details."
// @Failure 400 {object} ErrorResponse "Invalid machine ID (`invalid_input`)."
// @Failure 401 {object} ErrorResponse "Invalid or missing bearer credentials (`authentication_failed`)."
// @Failure 404 {object} ErrorResponse "The machine is unavailable to the caller (`machine_not_found`)."
// @Failure 500 {object} ErrorResponse "Unexpected server error (`internal_error`)."
// @Header all {string} Cache-Control "Always no-store."
// @Header 401 {string} WWW-Authenticate "Bearer"
// @Router /v1/machines/{id} [get]
func NewDetail(usecase Detailer) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		owner, ok := middleware.UserID(r.Context())
		if !ok {
			middleware.Unauthorized(w)
			return
		}
		machineID, err := handler.ParseUUID(chi.URLParam(r, "id"))
		if err != nil {
			handler.Error(w, http.StatusBadRequest, invalidInput, "Invalid machine ID")
			return
		}
		value, err := usecase.Detail(r.Context(), owner, machineID)
		switch {
		case errors.Is(err, machine.ErrNotFound):
			handler.Error(w, http.StatusNotFound, "machine_not_found", "Machine not found")
		case err != nil:
			handler.InternalError(w)
		default:
			handler.JSON(w, http.StatusOK, publicMachine(value))
		}
	})
}

func publicMachine(value machine.Machine) machineResponse {
	return machineResponse{ID: value.ID, Name: value.Name, CreatedAt: value.CreatedAt}
}
