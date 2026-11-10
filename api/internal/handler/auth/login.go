package auth

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/vladgrskkh/machine-whisperer/api/internal/domain/user"
	"github.com/vladgrskkh/machine-whisperer/api/internal/handler"
	"github.com/vladgrskkh/machine-whisperer/api/internal/usecase/auth"
)

type Login interface {
	Login(context.Context, string, string) (auth.TokenPair, error)
}

func NoStore(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		next.ServeHTTP(w, r)
	})
}

func NewLogin(usecase Login) http.Handler {
	return NoStore(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), signupTimeout)
		defer cancel()
		input, err := decodeCredentials(w, r)
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
		case errors.Is(err, auth.ErrRateLimited):
			retry := time.Second
			var limit *auth.RateLimitError
			if errors.As(err, &limit) && limit.RetryAfter > 0 {
				retry = limit.RetryAfter
			}
			seconds := int64(retry / time.Second)
			if retry%time.Second != 0 {
				seconds++
			}
			w.Header().Set("Retry-After", strconv.FormatInt(seconds, 10))
			handler.Error(w, http.StatusTooManyRequests, "rate_limited", "Too many requests")
		case err != nil:
			handler.Error(w, http.StatusInternalServerError, "internal_error", "An internal error occurred")
		default:
			handler.JSON(
				w,
				http.StatusOK,
				tokenResponse{
					AccessToken:  pair.AccessToken,
					TokenType:    "Bearer",
					ExpiresIn:    pair.ExpiresIn,
					RefreshToken: pair.RefreshToken,
				},
			)
		}
	}))
}
