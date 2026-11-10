package auth

import (
	"context"
	"errors"
	"time"

	"github.com/vladgrskkh/machine-whisperer/api/internal/domain/session"
)

type TransactionManager interface {
	WithinTransaction(ctx context.Context, fn func(context.Context) error) error
}

type SessionRepository interface {
	InsertSession(ctx context.Context, value session.Session) error
	InsertRefresh(ctx context.Context, value session.RefreshToken) error
}

type AccessSigner interface {
	Sign(ctx context.Context, claims session.AccessClaims) (string, error)
}

type RefreshGenerator interface {
	Generate(ctx context.Context) (string, [32]byte, error)
}

type Clock interface{ Now() time.Time }

var ErrRateLimited = errors.New("rate limited")

type EmailAttempts interface {
	Begin(ctx context.Context, email string) (finish func(bool), retryAfter time.Duration, err error)
}

type IDGenerator interface {
	Generate(ctx context.Context) (string, error)
}
