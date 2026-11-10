package application

import (
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	authhandler "github.com/vladgrskkh/machine-whisperer/api/internal/handler/auth"
	"github.com/vladgrskkh/machine-whisperer/api/internal/handler/middleware"
	"github.com/vladgrskkh/machine-whisperer/api/internal/infrastructure/auth/password"
	"github.com/vladgrskkh/machine-whisperer/api/internal/infrastructure/user/postgres"
	"github.com/vladgrskkh/machine-whisperer/api/internal/usecase/auth"
)

func NewHandler(pool *pgxpool.Pool, config Config) http.Handler {
	router := chi.NewRouter()
	usecase := auth.New(postgres.New(pool), password.Argon2id{})
	router.With(middleware.RateLimit(config.RateLimit)).
		Post("/v1/auth/signup", authhandler.New(usecase).ServeHTTP)
	return router
}
