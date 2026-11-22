package auth

import (
	"context"
	"errors"
	"net/http"

	"github.com/vladgrskkh/machine-whisperer/api/internal/domain/user"
	"github.com/vladgrskkh/machine-whisperer/api/internal/handler"
	"github.com/vladgrskkh/machine-whisperer/api/internal/handler/middleware"
	"github.com/vladgrskkh/machine-whisperer/api/internal/usecase/auth"
)

type Login interface {
	Login(context.Context, string, string) (auth.TokenPair, error)
}

func NewLogin(usecase Login) http.Handler {
	return middleware.NoStore(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), authTimeout)
		defer cancel()
		input, err := decodeAuthRequest[signupRequest](w, r)
		if err != nil {
			handler.Error(w, http.StatusBadRequest, "invalid_input", "Invalid email, password or request body")
			return
		}
		pair, err := usecase.Login(ctx, input.Email, input.Password)
		switch {
		case errors.Is(err, user.ErrInvalidCredentials):
			handler.Error(w, http.StatusBadRequest, "invalid_input", "Invalid email, password or request body")
		case errors.Is(err, auth.ErrAuthenticationFailed):
			handler.Error(w, http.StatusUnauthorized, "authentication_failed", "Authentication failed")
		case err != nil:
			handler.Error(w, http.StatusInternalServerError, "internal_error", "An internal error occurred")
		default:
			writeTokenPair(w, pair)
		}
	}))
}
