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
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	CreatedAt time.Time `json:"created_at"`
}

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
