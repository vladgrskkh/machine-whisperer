package application

import (
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/vladgrskkh/machine-whisperer/analytics/internal/handler/middleware"
	"github.com/vladgrskkh/machine-whisperer/analytics/internal/handler/telemetry"
)

func (a *Application) Routes() http.Handler {
	router := chi.NewRouter()
	bearer := middleware.Bearer(a.ingestKey)
	batchMiddleware := []func(http.Handler) http.Handler{bearer}
	if a.metrics != nil {
		router.Use(a.metrics.HTTP(router))
		batchMiddleware = []func(http.Handler) http.Handler{a.metrics.Batch, bearer}
	}
	router.Get("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	router.With(batchMiddleware...).
		Post("/internal/v1/telemetry-batches", telemetry.NewBatch(a.ingest).ServeHTTP)
	router.With(bearer).
		Get("/internal/v1/machines/{machine_id}/telemetry", telemetry.NewHistory(a.history).ServeHTTP)
	return router
}
