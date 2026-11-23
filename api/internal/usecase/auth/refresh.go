package auth

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"time"

	"github.com/vladgrskkh/machine-whisperer/api/internal/domain/session"
)

var ErrMalformedRefreshToken = errors.New("malformed refresh token")

type RefreshRepository interface {
	LockByRefreshHash(ctx context.Context, hash [32]byte) (session.Session, session.RefreshToken, error)
	ConsumeRefresh(ctx context.Context, hash [32]byte, at time.Time) error
	InsertRefresh(ctx context.Context, value session.RefreshToken) error
	RevokeSession(ctx context.Context, sessionID string, at time.Time) error
}

type RefreshRoleRepository interface {
	ListRoles(ctx context.Context, userID string) ([]string, error)
}

type Refresh struct {
	repository   RefreshRepository
	roles        RefreshRoleRepository
	transactions TransactionManager
	signer       AccessSigner
	refresh      RefreshGenerator
	ids          IDGenerator
	now          func() time.Time
}

func NewRefresh(
	repository RefreshRepository,
	roles RefreshRoleRepository,
	transactions TransactionManager,
	signer AccessSigner,
	refresh RefreshGenerator,
	ids IDGenerator,
	now func() time.Time,
) *Refresh {
	return &Refresh{
		repository:   repository,
		roles:        roles,
		transactions: transactions,
		signer:       signer,
		refresh:      refresh,
		ids:          ids,
		now:          now,
	}
}

func (u *Refresh) Refresh(ctx context.Context, raw string) (TokenPair, error) {
	hash, err := hashRefreshToken(raw)
	if err != nil {
		return TokenPair{}, err
	}

	var pair *TokenPair
	err = u.transactions.WithinTransaction(ctx, func(txCtx context.Context) error {
		value, current, lockErr := u.repository.LockByRefreshHash(txCtx, hash)
		if errors.Is(lockErr, session.ErrNotFound) {
			return nil
		}
		if lockErr != nil {
			return fmt.Errorf("lock refresh session: %w", lockErr)
		}
		now := u.now().UTC()
		if value.RevokedAt != nil || !now.Before(value.ExpiresAt) {
			return nil
		}
		if current.ConsumedAt != nil {
			// Let revocation commit before returning an authentication failure.
			return u.repository.RevokeSession(txCtx, value.ID, now)
		}
		next, rotateErr := u.rotate(txCtx, value, hash, now)
		if rotateErr != nil {
			return rotateErr
		}
		pair = &next
		return nil
	})
	if err != nil {
		return TokenPair{}, fmt.Errorf("rotate refresh token: %w", err)
	}
	if err = ctx.Err(); err != nil {
		return TokenPair{}, err
	}
	if pair == nil {
		return TokenPair{}, ErrAuthenticationFailed
	}
	return *pair, nil
}

func (u *Refresh) rotate(ctx context.Context, value session.Session, hash [32]byte, now time.Time) (TokenPair, error) {
	jti, err := u.ids.Generate(ctx)
	if err != nil {
		return TokenPair{}, fmt.Errorf("generate token ID: %w", err)
	}
	refresh, successorHash, err := u.refresh.Generate(ctx)
	if err != nil {
		return TokenPair{}, fmt.Errorf("generate refresh token: %w", err)
	}
	if err = u.repository.ConsumeRefresh(ctx, hash, now); err != nil {
		return TokenPair{}, fmt.Errorf("consume current refresh token: %w", err)
	}
	roles, err := u.roles.ListRoles(ctx, value.UserID)
	if err != nil {
		return TokenPair{}, fmt.Errorf("load roles for access token: %w", err)
	}
	access, err := u.signer.Sign(ctx, session.AccessClaims{
		UserID:    value.UserID,
		SessionID: value.ID,
		JTI:       jti,
		IssuedAt:  now,
		ExpiresAt: now.Add(accessLifetime),
		Roles:     roles,
	})
	if err != nil {
		return TokenPair{}, fmt.Errorf("sign access token: %w", err)
	}
	successor := session.RefreshToken{Hash: successorHash, SessionID: value.ID, CreatedAt: now}
	if err = u.repository.InsertRefresh(ctx, successor); err != nil {
		return TokenPair{}, fmt.Errorf("store successor refresh token: %w", err)
	}
	return TokenPair{AccessToken: access, RefreshToken: refresh, ExpiresIn: int(accessLifetime / time.Second)}, nil
}

func hashRefreshToken(raw string) ([32]byte, error) {
	const secretBytes = 32
	if len(raw) != base64.RawURLEncoding.EncodedLen(secretBytes) {
		return [32]byte{}, ErrMalformedRefreshToken
	}
	secret, err := base64.RawURLEncoding.Strict().DecodeString(raw)
	if err != nil || len(secret) != secretBytes {
		return [32]byte{}, ErrMalformedRefreshToken
	}
	return sha256.Sum256(secret), nil
}
