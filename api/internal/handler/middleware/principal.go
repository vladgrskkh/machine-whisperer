package middleware

import (
	"context"
	"net/http"
	"slices"

	"github.com/vladgrskkh/machine-whisperer/api/internal/handler"
)

type principalKind uint8

const (
	userPrincipal principalKind = iota + 1
	devicePrincipal
)

type Principal struct {
	kind      principalKind
	subject   string
	roles     []string
	machineID string
}

type principalKey struct{}

func withPrincipal(ctx context.Context, principal Principal) context.Context {
	return context.WithValue(ctx, principalKey{}, principal)
}

func principalFromContext(ctx context.Context) Principal {
	principal, _ := ctx.Value(principalKey{}).(Principal)
	return principal
}

func UserID(ctx context.Context) (string, bool) {
	principal := principalFromContext(ctx)
	if principal.kind != userPrincipal {
		return "", false
	}
	return principal.subject, principal.subject != ""
}

func DeviceBinding(ctx context.Context) (string, string, bool) {
	principal := principalFromContext(ctx)
	if principal.kind != devicePrincipal {
		return "", "", false
	}
	return principal.subject, principal.machineID, true
}

func Roles(ctx context.Context) []string {
	return slices.Clone(principalFromContext(ctx).roles)
}

func RequireRole(role string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !slices.Contains(principalFromContext(r.Context()).roles, role) {
				handler.Error(w, http.StatusForbidden, "forbidden", "Access denied")
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}
