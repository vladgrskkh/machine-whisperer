package auth

import (
	"context"
	"errors"
	"net/http"

	"github.com/vladgrskkh/machine-whisperer/api/internal/handler"
	"github.com/vladgrskkh/machine-whisperer/api/internal/usecase/auth"
)

type Logout interface {
	Logout(context.Context, string) error
}

// NewLogout handles refresh-token revocation.
//
// @Summary Revoke a user session
// @ID logOut
// @Tags auth
// @Description Revokes the session for a current or consumed refresh token.
// @Description Unknown and already revoked well-formed tokens also succeed.
// @Description Send one JSON object of at most 8 KiB. Malformed JSON, unknown fields and trailing values return 400.
// @Accept json
// @Produce json
// @Param body body RefreshToken true "Refresh token."
// @Success 204 "Session revoked or already unavailable."
// @Failure 400 {object} ErrorResponse "Malformed refresh token or request body (`invalid_input`)."
// @Failure 500 {object} ErrorResponse "Unexpected server error (`internal_error`)."
// @Router /v1/auth/logout [post]
func NewLogout(usecase Logout) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), authTimeout)
		defer cancel()
		input, err := decodeAuthRequest[refreshTokenRequest](w, r)
		if err != nil {
			handler.Error(w, http.StatusBadRequest, "invalid_input", invalidRefreshInput)
			return
		}
		err = usecase.Logout(ctx, input.RefreshToken)
		switch {
		case errors.Is(err, auth.ErrMalformedRefreshToken):
			handler.Error(w, http.StatusBadRequest, "invalid_input", invalidRefreshInput)
		case err != nil:
			handler.Error(w, http.StatusInternalServerError, "internal_error", "An internal error occurred")
		default:
			w.WriteHeader(http.StatusNoContent)
		}
	})
}
