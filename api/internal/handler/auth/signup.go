package auth

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/vladgrskkh/machine-whisperer/api/internal/domain/user"
	"github.com/vladgrskkh/machine-whisperer/api/internal/handler"
)

const authTimeout = 10 * time.Second

type Signup interface {
	Signup(context.Context, string, string) (user.User, error)
}

// New handles account signup.
//
// @Summary Register a user
// @ID signUp
// @Tags auth
// @Description Creates a user account without starting a session.
// @Description Send one JSON object of at most 8 KiB. Malformed JSON, unknown fields and trailing values return 400.
// @Description Shares a per-IP request budget with signup, login, refresh, device provisioning and device claim.
// @Accept json
// @Produce json
// @Param body body Credentials true "Email and password."
// @Success 201 {object} User "User created."
// @Failure 400 {object} ErrorResponse "Invalid email, password or request body (`invalid_input`)."
// @Failure 409 {object} ErrorResponse "Email is already registered (`email_exists`)."
// @Failure 429 {object} ErrorResponse "The shared per-IP request budget was exceeded (`rate_limited`)."
// @Failure 500 {object} ErrorResponse "Unexpected server error (`internal_error`)."
// @Header 429 {integer} Retry-After "Seconds until another attempt can be made."
// @Router /v1/auth/signup [post]
func New(usecase Signup) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), authTimeout)
		defer cancel()
		input, err := decodeAuthRequest[signupRequest](w, r)
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
