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

type RateLimitError struct{ RetryAfter time.Duration }

func (*RateLimitError) Error() string { return "rate limited" }
func (*RateLimitError) Unwrap() error { return ErrRateLimited }

type TokenPair struct {
	AccessToken, RefreshToken string
	ExpiresIn                 int
}

type LoginDependencies struct {
	Sessions     SessionRepository
	Transactions TransactionManager
	Signer       AccessSigner
	Refresh      RefreshGenerator
	IDs          IDGenerator
	Clock        Clock
	Attempts     EmailAttempts
	DummyHash    string
}

func NewWithLogin(repository Repository, hasher PasswordHasher, dependencies LoginDependencies) *Usecase {
	u := New(repository, hasher)
	u.login = dependencies
	return u
}

func (u *Usecase) Login(ctx context.Context, email, password string) (TokenPair, error) {
	canonical, err := user.ValidateCredentials(email, password)
	if err != nil {
		return TokenPair{}, err
	}
	finish, retry, err := u.login.Attempts.Begin(ctx, canonical)
	if err != nil {
		if errors.Is(err, ErrRateLimited) {
			return TokenPair{}, &RateLimitError{RetryAfter: retry}
		}
		return TokenPair{}, fmt.Errorf("begin login attempt: %w", err)
	}
	failed := false
	defer func() { finish(failed) }()
	account, hash, err := u.repository.FindCredentialsByEmail(ctx, canonical)
	unknown := errors.Is(err, user.ErrNotFound)
	if err != nil && !unknown {
		return TokenPair{}, fmt.Errorf("find login user: %w", err)
	}
	if unknown {
		hash = u.login.DummyHash
	}
	matches, err := u.hasher.Verify(ctx, password, hash)
	if err != nil {
		return TokenPair{}, fmt.Errorf("verify login password: %w", err)
	}
	if err = ctx.Err(); err != nil {
		return TokenPair{}, err
	}
	if unknown || !matches {
		failed = true
		return TokenPair{}, ErrAuthenticationFailed
	}
	return u.createSession(ctx, account.ID)
}

func (u *Usecase) createSession(ctx context.Context, userID string) (TokenPair, error) {
	id, err := u.login.IDs.Generate(ctx)
	if err != nil {
		return TokenPair{}, fmt.Errorf("generate session ID: %w", err)
	}
	jti, err := u.login.IDs.Generate(ctx)
	if err != nil {
		return TokenPair{}, fmt.Errorf("generate token ID: %w", err)
	}
	refresh, hash, err := u.login.Refresh.Generate(ctx)
	if err != nil {
		return TokenPair{}, fmt.Errorf("generate refresh token: %w", err)
	}
	now := u.login.Clock.Now().UTC()
	claims := session.AccessClaims{
		UserID:    userID,
		SessionID: id,
		JTI:       jti,
		IssuedAt:  now,
		ExpiresAt: now.Add(accessLifetime),
	}
	access, err := u.login.Signer.Sign(ctx, claims)
	if err != nil {
		return TokenPair{}, fmt.Errorf("sign access token: %w", err)
	}
	err = u.login.Transactions.WithinTransaction(ctx, func(txCtx context.Context) error {
		value := session.Session{ID: id, UserID: userID, CreatedAt: now, ExpiresAt: now.Add(sessionLifetime)}
		if insertErr := u.login.Sessions.InsertSession(txCtx, value); insertErr != nil {
			return fmt.Errorf("insert session: %w", insertErr)
		}
		token := session.RefreshToken{Hash: hash, SessionID: id, CreatedAt: now}
		if insertErr := u.login.Sessions.InsertRefresh(txCtx, token); insertErr != nil {
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
