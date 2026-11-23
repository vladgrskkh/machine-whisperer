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
	return router
}
