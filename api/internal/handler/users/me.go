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

// NewMe returns the handler for the authenticated user profile.
//
// @Summary Get the current user
// @ID getCurrentUser
// @Tags users
// @Produce json
// @Security UserAccessJWT
// @Success 200 {object} User "Current user."
// @Failure 401 {object} ErrorResponse "Invalid or missing bearer credentials, or the user no longer exists (`authentication_failed`)."
// @Failure 500 {object} ErrorResponse "Unexpected server error (`internal_error`)."
// @Header 401 {string} WWW-Authenticate "Bearer"
// @Router /v1/users/me [get]
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
