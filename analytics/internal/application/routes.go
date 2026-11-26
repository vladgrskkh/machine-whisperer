package application

import (
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/vladgrskkh/machine-whisperer/analytics/internal/handler/middleware"
	"github.com/vladgrskkh/machine-whisperer/analytics/internal/handler/telemetry"
	"github.com/vladgrskkh/machine-whisperer/analytics/internal/infrastructure/tracing"
)

func (a *Application) Routes() http.Handler {
	router := chi.NewRouter()
	router.Use(tracing.HTTPServer(a.tracerProvider, a.propagator))
	bearer := middleware.Bearer(a.ingestKey)
	if a.metrics != nil {
		router.Use(middleware.HTTP(router, a.metrics))
	}
	router.Get("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	router.With(bearer).
		Post("/internal/v1/telemetry-batches", telemetry.NewBatch(a.ingest).ServeHTTP)
	router.With(bearer).
		Get("/internal/v1/machines/{machine_id}/telemetry", telemetry.NewHistory(a.history).ServeHTTP)
	return router
}
