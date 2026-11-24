package application

import (
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/vladgrskkh/machine-whisperer/analytics/internal/handler/middleware"
	"github.com/vladgrskkh/machine-whisperer/analytics/internal/handler/telemetry"
)

func (a *Application) Routes() http.Handler {
	router := chi.NewRouter()
	router.With(middleware.Bearer(a.ingestKey)).
		Post("/internal/v1/telemetry-batches", telemetry.NewBatch(a.ingest).ServeHTTP)
	router.With(middleware.Bearer(a.ingestKey)).
		Get("/internal/v1/machines/{machine_id}/telemetry", telemetry.NewHistory(a.history).ServeHTTP)
	return router
}
