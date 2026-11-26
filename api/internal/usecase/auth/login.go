package auth

import (
	"context"
	"errors"
	"fmt"
	"time"

	"go.opentelemetry.io/otel/trace"

	"github.com/vladgrskkh/machine-whisperer/api/internal/domain/session"
	"github.com/vladgrskkh/machine-whisperer/api/internal/domain/user"
	"github.com/vladgrskkh/machine-whisperer/api/internal/spanutil"
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
	FindCredentialsByEmail(ctx context.Context, email string) (user.Credentials, error)
	ListRoles(ctx context.Context, userID string) ([]string, error)
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

type Login struct {
	tracer       trace.Tracer
	repository   LoginRepository
	verifier     PasswordVerifier
	sessions     SessionRepository
	transactions TransactionManager
	signer       AccessSigner
	refresh      RefreshGenerator
	ids          IDGenerator
	now          func() time.Time
	dummyHash    string
}

func NewLogin(
	repository LoginRepository,
	verifier PasswordVerifier,
	sessions SessionRepository,
	transactions TransactionManager,
	signer AccessSigner,
	refresh RefreshGenerator,
	ids IDGenerator,
	now func() time.Time,
	dummyHash string,
	tracer trace.Tracer,
) *Login {
	return &Login{
		tracer: tracer, repository: repository, verifier: verifier, sessions: sessions, transactions: transactions,
		signer: signer, refresh: refresh, ids: ids, now: now, dummyHash: dummyHash,
	}
}

func (u *Login) Login(ctx context.Context, email, password string) (_ TokenPair, err error) {
	ctx, span := u.tracer.Start(ctx, "auth.login")
	defer func() { spanutil.End(span, err, user.ErrInvalidCredentials, ErrAuthenticationFailed) }()
	canonical, err := user.ValidateCredentials(email, password)
	if err != nil {
		return TokenPair{}, err
	}
	if err = ctx.Err(); err != nil {
		return TokenPair{}, err
	}
	credentials, err := u.repository.FindCredentialsByEmail(ctx, canonical)
	unknown := errors.Is(err, user.ErrNotFound)
	if err != nil && !unknown {
		return TokenPair{}, fmt.Errorf("find login user: %w", err)
	}
	if unknown {
		credentials.PasswordHash = u.dummyHash
	}
	matches, err := u.verifier.Verify(ctx, password, credentials.PasswordHash)
	if err != nil {
		return TokenPair{}, fmt.Errorf("verify login password: %w", err)
	}
	if err = ctx.Err(); err != nil {
		return TokenPair{}, err
	}
	if unknown || !matches {
		return TokenPair{}, ErrAuthenticationFailed
	}
	issuedAt := u.now().UTC()
	roles, err := u.repository.ListRoles(ctx, credentials.UserID)
	if err != nil {
		return TokenPair{}, fmt.Errorf("load roles for access token: %w", err)
	}
	if err = ctx.Err(); err != nil {
		return TokenPair{}, err
	}
	return u.createSession(ctx, credentials.UserID, roles, issuedAt)
}

func (u *Login) createSession(
	ctx context.Context,
	userID string,
	roles []string,
	issuedAt time.Time,
) (TokenPair, error) {
	id, err := u.ids.Generate(ctx)
	if err != nil {
		return TokenPair{}, fmt.Errorf("generate session ID: %w", err)
	}
	jti, err := u.ids.Generate(ctx)
	if err != nil {
		return TokenPair{}, fmt.Errorf("generate token ID: %w", err)
	}
	refresh, hash, err := u.refresh.Generate(ctx)
	if err != nil {
		return TokenPair{}, fmt.Errorf("generate refresh token: %w", err)
	}
	claims := session.AccessClaims{
		UserID:    userID,
		SessionID: id,
		JTI:       jti,
		IssuedAt:  issuedAt,
		ExpiresAt: issuedAt.Add(accessLifetime),
		Roles:     roles,
	}
	access, err := u.signer.Sign(ctx, claims)
	if err != nil {
		return TokenPair{}, fmt.Errorf("sign access token: %w", err)
	}
	err = u.transactions.WithinTransaction(ctx, func(txCtx context.Context) error {
		value := session.Session{ID: id, UserID: userID, CreatedAt: issuedAt, ExpiresAt: issuedAt.Add(sessionLifetime)}
		if insertErr := u.sessions.InsertSession(txCtx, value); insertErr != nil {
			return fmt.Errorf("insert session: %w", insertErr)
		}
		token := session.RefreshToken{Hash: hash, SessionID: id, CreatedAt: issuedAt}
		if insertErr := u.sessions.InsertRefresh(txCtx, token); insertErr != nil {
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
