package auth

import (
	"context"
	"errors"
	"net/http"

	"github.com/vladgrskkh/machine-whisperer/api/internal/handler"
	"github.com/vladgrskkh/machine-whisperer/api/internal/handler/middleware"
	"github.com/vladgrskkh/machine-whisperer/api/internal/usecase/auth"
)

const invalidRefreshInput = "Invalid refresh token or request body"

type Refresh interface {
	Refresh(context.Context, string) (auth.TokenPair, error)
}

func NewRefresh(usecase Refresh) http.Handler {
	return middleware.NoStore(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), authTimeout)
		defer cancel()
		input, err := decodeAuthRequest[refreshTokenRequest](w, r)
		if err != nil {
			handler.Error(w, http.StatusBadRequest, "invalid_input", invalidRefreshInput)
			return
		}
		pair, err := usecase.Refresh(ctx, input.RefreshToken)
		switch {
		case errors.Is(err, auth.ErrMalformedRefreshToken):
			handler.Error(w, http.StatusBadRequest, "invalid_input", invalidRefreshInput)
		case errors.Is(err, auth.ErrAuthenticationFailed):
			handler.Error(w, http.StatusUnauthorized, "authentication_failed", "Authentication failed")
		case err != nil:
			handler.Error(w, http.StatusInternalServerError, "internal_error", "An internal error occurred")
		default:
			writeTokenPair(w, pair)
		}
	}))
}
