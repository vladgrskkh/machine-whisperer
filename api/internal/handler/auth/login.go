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

// NewLogin handles password login.
//
// @Summary Start a user session
// @ID logIn
// @Tags auth
// @Description Starts an independent session and returns a user access JWT and refresh token.
// @Description Send one JSON object of at most 8 KiB. Malformed JSON, unknown fields and trailing values return 400.
// @Description Shares a per-IP request budget with signup, login, refresh, device provisioning and device claim.
// @Accept json
// @Produce json
// @Param body body Credentials true "Email and password."
// @Success 200 {object} TokenPair "Session tokens."
// @Failure 400 {object} ErrorResponse "Invalid email, password or request body (`invalid_input`)."
// @Failure 401 {object} ErrorResponse "Unknown email or incorrect password (`authentication_failed`)."
// @Failure 429 {object} ErrorResponse "The shared per-IP request budget was exceeded (`rate_limited`)."
// @Failure 500 {object} ErrorResponse "Unexpected server error (`internal_error`)."
// @Header all {string} Cache-Control "Always no-store."
// @Header 429 {integer} Retry-After "Seconds until another attempt can be made."
// @Router /v1/auth/login [post]
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
