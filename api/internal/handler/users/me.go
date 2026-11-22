package users

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/vladgrskkh/machine-whisperer/api/internal/domain/user"
	"github.com/vladgrskkh/machine-whisperer/api/internal/handler"
	"github.com/vladgrskkh/machine-whisperer/api/internal/handler/middleware"
	"github.com/vladgrskkh/machine-whisperer/api/internal/usecase/auth"
)

type CurrentUser interface {
	Me(context.Context, string) (user.User, error)
}

type userResponse struct {
	ID        string    `json:"id"`
	Email     string    `json:"email"`
	CreatedAt time.Time `json:"created_at"`
}

func NewMe(usecase CurrentUser) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, ok := middleware.UserID(r.Context())
		if !ok {
			unauthorized(w)
			return
		}
		current, err := usecase.Me(r.Context(), id)
		switch {
		case errors.Is(err, auth.ErrAuthenticationFailed):
			unauthorized(w)
		case err != nil:
			handler.Error(w, http.StatusInternalServerError, "internal_error", "An internal error occurred")
		default:
			handler.JSON(
				w,
				http.StatusOK,
				userResponse{ID: current.ID, Email: current.Email, CreatedAt: current.CreatedAt},
			)
		}
	})
}

func unauthorized(w http.ResponseWriter) {
	w.Header().Set("WWW-Authenticate", "Bearer")
	handler.Error(w, http.StatusUnauthorized, "authentication_failed", "Authentication failed")
}
