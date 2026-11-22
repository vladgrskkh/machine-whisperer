package auth

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/vladgrskkh/machine-whisperer/api/internal/domain/session"
	"github.com/vladgrskkh/machine-whisperer/api/internal/domain/user"
)

const (
	accessLifetime  = 15 * time.Minute
	sessionLifetime = 30 * 24 * time.Hour
)

var ErrAuthenticationFailed = errors.New("authentication failed")

type TokenPair struct {
	AccessToken, RefreshToken string
	ExpiresIn                 int
}

type LoginRepository interface {
	FindCredentialsByEmail(ctx context.Context, email string) (user.User, string, error)
}

type PasswordVerifier interface {
	Verify(ctx context.Context, password, hash string) (bool, error)
}

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

type IDGenerator interface {
	Generate(ctx context.Context) (string, error)
}

type LoginDependencies struct {
	Sessions     SessionRepository
	Transactions TransactionManager
	Signer       AccessSigner
	Refresh      RefreshGenerator
	IDs          IDGenerator
	Now          func() time.Time
	DummyHash    string
}

type Login struct {
	repository   LoginRepository
	verifier     PasswordVerifier
	dependencies LoginDependencies
}

func NewLogin(repository LoginRepository, verifier PasswordVerifier, dependencies LoginDependencies) *Login {
	return &Login{repository: repository, verifier: verifier, dependencies: dependencies}
}

func (u *Login) Login(ctx context.Context, email, password string) (TokenPair, error) {
	canonical, err := user.ValidateCredentials(email, password)
	if err != nil {
		return TokenPair{}, err
	}
	if err = ctx.Err(); err != nil {
		return TokenPair{}, err
	}
	account, hash, err := u.repository.FindCredentialsByEmail(ctx, canonical)
	unknown := errors.Is(err, user.ErrNotFound)
	if err != nil && !unknown {
		return TokenPair{}, fmt.Errorf("find login user: %w", err)
	}
	if unknown {
		hash = u.dependencies.DummyHash
	}
	matches, err := u.verifier.Verify(ctx, password, hash)
	if err != nil {
		return TokenPair{}, fmt.Errorf("verify login password: %w", err)
	}
	if err = ctx.Err(); err != nil {
		return TokenPair{}, err
	}
	if unknown || !matches {
		return TokenPair{}, ErrAuthenticationFailed
	}
	return u.createSession(ctx, account.ID)
}

func (u *Login) createSession(ctx context.Context, userID string) (TokenPair, error) {
	id, err := u.dependencies.IDs.Generate(ctx)
	if err != nil {
		return TokenPair{}, fmt.Errorf("generate session ID: %w", err)
	}
	jti, err := u.dependencies.IDs.Generate(ctx)
	if err != nil {
		return TokenPair{}, fmt.Errorf("generate token ID: %w", err)
	}
	refresh, hash, err := u.dependencies.Refresh.Generate(ctx)
	if err != nil {
		return TokenPair{}, fmt.Errorf("generate refresh token: %w", err)
	}
	now := u.dependencies.Now().UTC()
	claims := session.AccessClaims{
		UserID:    userID,
		SessionID: id,
		JTI:       jti,
		IssuedAt:  now,
		ExpiresAt: now.Add(accessLifetime),
	}
	access, err := u.dependencies.Signer.Sign(ctx, claims)
	if err != nil {
		return TokenPair{}, fmt.Errorf("sign access token: %w", err)
	}
	err = u.dependencies.Transactions.WithinTransaction(ctx, func(txCtx context.Context) error {
		value := session.Session{ID: id, UserID: userID, CreatedAt: now, ExpiresAt: now.Add(sessionLifetime)}
		if insertErr := u.dependencies.Sessions.InsertSession(txCtx, value); insertErr != nil {
			return fmt.Errorf("insert session: %w", insertErr)
		}
		token := session.RefreshToken{Hash: hash, SessionID: id, CreatedAt: now}
		if insertErr := u.dependencies.Sessions.InsertRefresh(txCtx, token); insertErr != nil {
			return fmt.Errorf("insert refresh token: %w", insertErr)
		}
		return txCtx.Err()
	})
	if err != nil {
		return TokenPair{}, fmt.Errorf("create login session: %w", err)
	}
	if err = ctx.Err(); err != nil {
		return TokenPair{}, err
	}
	return TokenPair{AccessToken: access, RefreshToken: refresh, ExpiresIn: int(accessLifetime / time.Second)}, nil
}
