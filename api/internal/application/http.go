package application

import (
	"net/http"

	"github.com/go-chi/chi/v5"

	authhandler "github.com/vladgrskkh/machine-whisperer/api/internal/handler/auth"
	"github.com/vladgrskkh/machine-whisperer/api/internal/handler/middleware"
	"github.com/vladgrskkh/machine-whisperer/api/internal/handler/users"
)

func (a *Application) Routes() http.Handler {
	router := chi.NewRouter()
	limit := middleware.RateLimit(a.rateLimitConfig)
	router.With(limit).Post("/v1/auth/signup", authhandler.New(a.signup).ServeHTTP)
	router.Route("/v1/auth/login", func(login chi.Router) {
		login.Use(middleware.NoStore)
		login.With(limit).Post("/", authhandler.NewLogin(a.login).ServeHTTP)
	})
	router.Route("/v1/auth/refresh", func(refresh chi.Router) {
		refresh.Use(middleware.NoStore)
		refresh.With(limit).Post("/", authhandler.NewRefresh(a.refresh).ServeHTTP)
	})
	router.Post("/v1/auth/logout", authhandler.NewLogout(a.logout).ServeHTTP)
	router.With(middleware.AccessToken(a.verifier)).Get("/v1/users/me", users.NewMe(a.me).ServeHTTP)

	return router
}
