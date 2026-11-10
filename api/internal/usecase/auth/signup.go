package auth

import (
	"context"
	"fmt"

	"github.com/vladgrskkh/machine-whisperer/api/internal/domain/user"
)

type Repository interface {
	Create(ctx context.Context, email, passwordHash string) (user.User, error)
}

type PasswordHasher interface {
	Hash(ctx context.Context, password string) (string, error)
}

type Usecase struct {
	repository Repository
	hasher     PasswordHasher
}

func New(repository Repository, hasher PasswordHasher) *Usecase {
	return &Usecase{repository: repository, hasher: hasher}
}

func (u *Usecase) Signup(ctx context.Context, email, password string) (user.User, error) {
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
