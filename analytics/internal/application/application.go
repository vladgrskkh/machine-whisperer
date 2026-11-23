package application

import (
	"context"
	"errors"

	"github.com/vladgrskkh/machine-whisperer/analytics/internal/config"
	"github.com/vladgrskkh/machine-whisperer/analytics/internal/usecase/telemetry"
)

type Application struct {
	ingestKey       string
	repository      telemetry.TelemetryRepository
	ingest          *telemetry.IngestBatch
	closeClickHouse func() error
}

type Option func(context.Context, *Application) error

func New(ctx context.Context, cfg config.Config, options ...Option) (*Application, error) {
	app := &Application{ingestKey: cfg.IngestKey}
	for _, option := range options {
		if err := option(ctx, app); err != nil {
			app.Close()
			return nil, err
		}
	}
	if app.repository == nil {
		return nil, errors.New("telemetry repository is required")
	}
	app.ingest = telemetry.NewIngestBatch(app.repository)
	return app, nil
}

func WithTelemetryRepository(repository telemetry.TelemetryRepository) Option {
	return func(_ context.Context, app *Application) error {
		if repository == nil || app.repository != nil {
			return errors.New("invalid telemetry repository configuration")
		}
		app.repository = repository
		return nil
	}
}

func (a *Application) Close() {
	if a.closeClickHouse != nil {
		_ = a.closeClickHouse()
	}
}
