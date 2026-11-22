package application

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/vladgrskkh/machine-whisperer/api/internal/handler/middleware"
	"github.com/vladgrskkh/machine-whisperer/api/internal/infrastructure/auth/password"
	"github.com/vladgrskkh/machine-whisperer/api/internal/infrastructure/auth/token"
	sessionpostgres "github.com/vladgrskkh/machine-whisperer/api/internal/infrastructure/session/postgres"
	transactionpostgres "github.com/vladgrskkh/machine-whisperer/api/internal/infrastructure/transaction/postgres"
	"github.com/vladgrskkh/machine-whisperer/api/internal/infrastructure/user/postgres"
	"github.com/vladgrskkh/machine-whisperer/api/internal/usecase/auth"
)

const authStartupTimeout = 10 * time.Second

type Application struct {
	pool      *pgxpool.Pool
	ownsPool  bool
	signup    *auth.Signup
	login     *auth.Login
	me        *auth.Me
	verifier  *token.JWT
	rateLimit func(http.Handler) http.Handler
}

type Option func(context.Context, *Application) error

func New(ctx context.Context, config Config, options ...Option) (*Application, error) {
	startupCtx, cancel := context.WithTimeout(ctx, authStartupTimeout)
	defer cancel()
	if startupCtx.Err() != nil {
		return nil, errors.New("auth initialization failed")
	}
	keys, err := token.LoadKeys(config.Auth.SigningKeyID, config.Auth.SigningKeyFile, config.Auth.VerifyKeysFile)
	if err != nil {
		return nil, errors.New("invalid auth key configuration")
	}
	now := config.Auth.Now
	if now == nil {
		now = time.Now
	}
	signer, err := token.NewJWT(keys, now)
	if err != nil {
		return nil, errors.New("invalid auth key configuration")
	}
	hasher := password.Argon2id{}
	dummy, err := hasher.Hash(startupCtx, "fixed non-user dummy password material")
	if err != nil {
		return nil, errors.New("auth initialization failed")
	}

	app := &Application{verifier: signer, rateLimit: middleware.RateLimit(config.RateLimits.IP)}
	for _, option := range options {
		if err = option(ctx, app); err != nil {
			app.Close()
			return nil, err
		}
	}
	if app.pool == nil {
		return nil, errors.New("database pool is required")
	}
	repository := postgres.New(app.pool)
	app.signup = auth.NewSignup(repository, hasher)
	app.login = auth.NewLogin(repository, hasher, auth.LoginDependencies{
		Sessions:     sessionpostgres.New(app.pool),
		Transactions: transactionpostgres.New(app.pool),
		Signer:       signer,
		Refresh:      token.Refresh{},
		IDs:          token.IDs{},
		Now:          now,
		DummyHash:    dummy,
	})
	app.me = auth.NewMe(repository)

	return app, nil
}

func (a *Application) Close() {
	if a.ownsPool {
		a.pool.Close()
	}
}
