package auth

import (
	"context"
	"errors"
	"fmt"

	"go.opentelemetry.io/otel/trace"

	"github.com/vladgrskkh/machine-whisperer/api/internal/domain/user"
	"github.com/vladgrskkh/machine-whisperer/api/internal/spanutil"
)

type MeRepository interface {
	FindByID(ctx context.Context, id string) (user.User, error)
}

type Me struct {
	tracer     trace.Tracer
	repository MeRepository
}

func NewMe(repository MeRepository, tracer trace.Tracer) *Me {
	return &Me{tracer: tracer, repository: repository}
}

//nolint:nonamedreturns // Span completion observes the returned error.
func (u *Me) Me(ctx context.Context, userID string) (_ user.User, err error) {
	ctx, span := u.tracer.Start(ctx, "auth.me")
	defer func() { spanutil.End(span, err, ErrAuthenticationFailed) }()
	account, err := u.repository.FindByID(ctx, userID)
	if errors.Is(err, user.ErrNotFound) {
		return user.User{}, ErrAuthenticationFailed
	}
	if err != nil {
		return user.User{}, fmt.Errorf("find current user: %w", err)
	}
	return account, nil
}
