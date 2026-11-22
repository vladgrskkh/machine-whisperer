package auth

import (
	"context"
	"errors"
	"fmt"

	"github.com/vladgrskkh/machine-whisperer/api/internal/domain/user"
)

type MeRepository interface {
	FindByID(ctx context.Context, id string) (user.User, error)
}

type Me struct {
	repository MeRepository
}

func NewMe(repository MeRepository) *Me {
	return &Me{repository: repository}
}

func (u *Me) Me(ctx context.Context, userID string) (user.User, error) {
	account, err := u.repository.FindByID(ctx, userID)
	if errors.Is(err, user.ErrNotFound) {
		return user.User{}, ErrAuthenticationFailed
	}
	if err != nil {
		return user.User{}, fmt.Errorf("find current user: %w", err)
	}
	return account, nil
}
