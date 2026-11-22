package auth

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/vladgrskkh/machine-whisperer/api/internal/domain/session"
)

type LogoutRepository interface {
	LockByRefreshHash(ctx context.Context, hash [32]byte) (session.Session, session.RefreshToken, error)
	RevokeSession(ctx context.Context, sessionID string, at time.Time) error
}

type Logout struct {
	repository   LogoutRepository
	transactions TransactionManager
	now          func() time.Time
}

func NewLogout(repository LogoutRepository, transactions TransactionManager, now func() time.Time) *Logout {
	return &Logout{repository: repository, transactions: transactions, now: now}
}

func (u *Logout) Logout(ctx context.Context, raw string) error {
	hash, err := hashRefreshToken(raw)
	if err != nil {
		return err
	}

	err = u.transactions.WithinTransaction(ctx, func(txCtx context.Context) error {
		value, _, lockErr := u.repository.LockByRefreshHash(txCtx, hash)
		if errors.Is(lockErr, session.ErrNotFound) {
			return nil
		}
		if lockErr != nil {
			return fmt.Errorf("lock logout session: %w", lockErr)
		}
		if value.RevokedAt != nil {
			return nil
		}
		return u.repository.RevokeSession(txCtx, value.ID, u.now().UTC())
	})
	if err != nil {
		return fmt.Errorf("logout session: %w", err)
	}
	return nil
}
