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
