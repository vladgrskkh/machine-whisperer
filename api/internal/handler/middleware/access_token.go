package middleware

import (
	"context"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/vladgrskkh/machine-whisperer/api/internal/domain/session"
	"github.com/vladgrskkh/machine-whisperer/api/internal/handler"
)

const maxBearerBytes = 8 * 1024
const accessTimeout = 10 * time.Second

type AccessVerifier interface {
	Verify(context.Context, string) (session.AccessClaims, error)
}

func AccessToken(verifier AccessVerifier) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			values := r.Header.Values("Authorization")
			if len(values) != 1 {
				Unauthorized(w)
				return
			}
			scheme, raw, ok := strings.Cut(values[0], " ")
			if !ok || !strings.EqualFold(scheme, "Bearer") || len(raw) == 0 || len(raw) > maxBearerBytes ||
				strings.ContainsAny(raw, " \t\r\n,") {
				Unauthorized(w)
				return
			}
			ctx, cancel := context.WithTimeout(r.Context(), accessTimeout)
			defer cancel()
			claims, err := verifier.Verify(ctx, raw)
			if err != nil {
				Unauthorized(w)
				return
			}
			principal := Principal{kind: userPrincipal, subject: claims.UserID, roles: slices.Clone(claims.Roles)}
			next.ServeHTTP(w, r.WithContext(withPrincipal(ctx, principal)))
		})
	}
}

func Unauthorized(w http.ResponseWriter) {
	w.Header().Set("WWW-Authenticate", "Bearer")
	handler.Error(w, http.StatusUnauthorized, "authentication_failed", "Authentication failed")
}
