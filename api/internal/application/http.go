package application

import (
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/httplog/v3"

	"github.com/vladgrskkh/machine-whisperer/api/internal/domain/user"
	authhandler "github.com/vladgrskkh/machine-whisperer/api/internal/handler/auth"
	"github.com/vladgrskkh/machine-whisperer/api/internal/handler/devices"
	"github.com/vladgrskkh/machine-whisperer/api/internal/handler/machines"
	"github.com/vladgrskkh/machine-whisperer/api/internal/handler/middleware"
	"github.com/vladgrskkh/machine-whisperer/api/internal/handler/users"
)

func (a *Application) Routes() http.Handler {
	router := chi.NewRouter()
	schema := *httplog.SchemaECS
	schema.RequestURL = ""
	schema.RequestReferer = ""
	router.Use(httplog.RequestLogger(a.logger, &httplog.Options{
		Level:              slog.LevelDebug,
		Schema:             &schema,
		LogRequestHeaders:  []string{"Content-Type"},
		LogResponseHeaders: []string{},
		LogFormat: func(r *http.Request, _ *httplog.LogFormatArgs) string {
			return r.Method + " " + r.URL.Path
		},
	}))
	limit := middleware.RateLimit(a.rateLimitConfig)
	access := middleware.AccessToken(a.verifier)
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
	router.With(access).Get("/v1/users/me", users.NewMe(a.me).ServeHTTP)
	router.With(middleware.NoStore, limit, access, middleware.RequireRole(user.RoleDeviceProvisioner)).
		Post("/internal/v1/device-provisioning", devices.NewProvision(a.provision).ServeHTTP)
	router.With(middleware.NoStore, limit, access).Post("/v1/devices/claim", devices.NewClaim(a.claim).ServeHTTP)
	router.Group(func(owner chi.Router) {
		owner.Use(middleware.NoStore, access)
		owner.Post("/v1/machines", machines.NewCreate(a.createMachine).ServeHTTP)
		owner.Get("/v1/machines", machines.NewList(a.listMachines).ServeHTTP)
		owner.Get("/v1/machines/{id}", machines.NewDetail(a.machineDetail).ServeHTTP)
	})

	return router
}
