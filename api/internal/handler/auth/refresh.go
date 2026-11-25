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

// NewRefresh handles refresh-token rotation.
//
// @Summary Rotate session tokens
// @ID refreshSession
// @Tags auth
// @Description Rotates the refresh token within the existing session and returns a replacement token pair.
// @Description Reusing a consumed token revokes its session if it is still active.
// @Description Send one JSON object of at most 8 KiB. Malformed JSON, unknown fields and trailing values return 400.
// @Description Shares a per-IP request budget with signup, login, refresh, device provisioning and device claim.
// @Accept json
// @Produce json
// @Param body body RefreshToken true "Current refresh token."
// @Success 200 {object} TokenPair "Replacement session tokens."
// @Failure 400 {object} ErrorResponse "Malformed refresh token or request body (`invalid_input`)."
// @Failure 401 {object} ErrorResponse "Unknown, expired, revoked or reused refresh token (`authentication_failed`)."
// @Failure 429 {object} ErrorResponse "The shared per-IP request budget was exceeded (`rate_limited`)."
// @Failure 500 {object} ErrorResponse "Unexpected server error (`internal_error`)."
// @Header all {string} Cache-Control "Always no-store."
// @Header 429 {integer} Retry-After "Seconds until another attempt can be made."
// @Router /v1/auth/refresh [post]
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
