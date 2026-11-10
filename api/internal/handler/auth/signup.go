package auth

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/vladgrskkh/machine-whisperer/api/internal/domain/user"
	"github.com/vladgrskkh/machine-whisperer/api/internal/handler"
	"github.com/vladgrskkh/machine-whisperer/api/internal/usecase/auth"
)

const (
	signupTimeout = 10 * time.Second
)

func New(usecase *auth.Usecase) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), signupTimeout)
		defer cancel()
		input, err := decodeCredentials(w, r)
		if err != nil {
			handler.Error(w, http.StatusBadRequest, "invalid_input", "Invalid email, password or request body")
			return
		}
		created, err := usecase.Signup(ctx, input.Email, input.Password)
		switch {
		case errors.Is(err, user.ErrInvalidCredentials):
			handler.Error(w, http.StatusBadRequest, "invalid_input", "Invalid email, password or request body")
		case errors.Is(err, user.ErrEmailExists):
			handler.Error(w, http.StatusConflict, "email_exists", "Email is already registered")
		case err != nil:
			handler.Error(w, http.StatusInternalServerError, "internal_error", "An internal error occurred")
		default:
			handler.JSON(
				w,
				http.StatusCreated,
				userResponse{ID: created.ID, Email: created.Email, CreatedAt: created.CreatedAt},
			)
		}
	})
}
