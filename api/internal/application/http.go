package application

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	authhandler "github.com/vladgrskkh/machine-whisperer/api/internal/handler/auth"
	"github.com/vladgrskkh/machine-whisperer/api/internal/handler/middleware"
	"github.com/vladgrskkh/machine-whisperer/api/internal/handler/users"
	"github.com/vladgrskkh/machine-whisperer/api/internal/infrastructure/auth/attempts"
	"github.com/vladgrskkh/machine-whisperer/api/internal/infrastructure/auth/password"
	"github.com/vladgrskkh/machine-whisperer/api/internal/infrastructure/auth/token"
	sessionpostgres "github.com/vladgrskkh/machine-whisperer/api/internal/infrastructure/session/postgres"
	transactionpostgres "github.com/vladgrskkh/machine-whisperer/api/internal/infrastructure/transaction/postgres"
	"github.com/vladgrskkh/machine-whisperer/api/internal/infrastructure/user/postgres"
	"github.com/vladgrskkh/machine-whisperer/api/internal/usecase/auth"
)

const authStartupTimeout = 10 * time.Second

type clock struct{ now func() time.Time }

func (c clock) Now() time.Time { return c.now() }

func NewHandler(ctx context.Context, pool *pgxpool.Pool, config Config) (http.Handler, error) {
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
	emailConfig := config.RateLimits.Email
	if emailConfig.Now == nil {
		emailConfig.Now = now
	}
	usecase := auth.NewWithLogin(postgres.New(pool), hasher, auth.LoginDependencies{
		Sessions:     sessionpostgres.New(pool),
		Transactions: transactionpostgres.New(pool),
		Signer:       signer,
		Refresh:      token.Refresh{},
		IDs:          token.IDs{},
		Clock:        clock{now: now},
		Attempts:     attempts.New(emailConfig),
		DummyHash:    dummy,
	})
	router := chi.NewRouter()
	limit := middleware.RateLimit(config.RateLimits.IP)
	router.With(limit).Post("/v1/auth/signup", authhandler.New(usecase).ServeHTTP)
	router.Route("/v1/auth/login", func(login chi.Router) {
		login.Use(middleware.NoStore)
		login.With(limit).Post("/", authhandler.NewLogin(usecase).ServeHTTP)
	})
	router.With(middleware.AccessToken(signer)).Get("/v1/users/me", users.NewMe(usecase).ServeHTTP)
	return router, nil
}
