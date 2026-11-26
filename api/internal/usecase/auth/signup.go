package auth

import (
	"context"
	"fmt"

	"go.opentelemetry.io/otel/trace"

	"github.com/vladgrskkh/machine-whisperer/api/internal/domain/user"
	"github.com/vladgrskkh/machine-whisperer/api/internal/spanutil"
)

type SignupRepository interface {
	Create(ctx context.Context, email, passwordHash string) (user.User, error)
}

type PasswordHasher interface {
	Hash(ctx context.Context, password string) (string, error)
}

type Signup struct {
	tracer     trace.Tracer
	repository SignupRepository
	hasher     PasswordHasher
}

func NewSignup(repository SignupRepository, hasher PasswordHasher, tracer trace.Tracer) *Signup {
	return &Signup{tracer: tracer, repository: repository, hasher: hasher}
}

func (u *Signup) Signup(ctx context.Context, email, password string) (_ user.User, err error) {
	ctx, span := u.tracer.Start(ctx, "auth.signup")
	defer func() { spanutil.End(span, err, user.ErrInvalidCredentials, user.ErrEmailExists) }()
	canonical, err := user.ValidateCredentials(email, password)
	if err != nil {
		return user.User{}, err
	}
	hash, err := u.hasher.Hash(ctx, password)
	if err != nil {
		return user.User{}, fmt.Errorf("hash password: %w", err)
	}
	created, err := u.repository.Create(ctx, canonical, hash)
	if err != nil {
		return user.User{}, fmt.Errorf("create user: %w", err)
	}
	return created, nil
}
