package application

import (
	"context"
	"errors"
	"net/http"
	"time"

	ch "github.com/ClickHouse/clickhouse-go/v2"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
	"go.opentelemetry.io/otel/trace/noop"

	"github.com/vladgrskkh/machine-whisperer/analytics/internal/config"
	"github.com/vladgrskkh/machine-whisperer/analytics/internal/infrastructure/clickhouse"
	"github.com/vladgrskkh/machine-whisperer/analytics/internal/infrastructure/metrics"
	"github.com/vladgrskkh/machine-whisperer/analytics/internal/usecase/telemetry"
)

const connectTimeout = 10 * time.Second

type Application struct {
	MetricsHandler http.Handler

	ingestKey      string
	clickHouse     ch.Conn
	metrics        *metrics.Metrics
	ingest         *telemetry.IngestBatch
	history        *telemetry.ListHistory
	tracerProvider trace.TracerProvider
	propagator     propagation.TextMapPropagator
}

type Option func(context.Context, *Application) error

func New(ctx context.Context, cfg config.Config, options ...Option) (*Application, error) {
	app := &Application{
		ingestKey: cfg.IngestKey, tracerProvider: noop.NewTracerProvider(), propagator: propagation.TraceContext{},
	}
	for _, option := range options {
		if err := option(ctx, app); err != nil {
			app.Close()
			return nil, err
		}
	}
	if app.clickHouse == nil {
		return nil, errors.New("telemetry repository is required")
	}
	app.metrics = metrics.New()
	app.MetricsHandler = app.metrics.Handler()
	repository := clickhouse.NewRepository(app.clickHouse, app.metrics)
	app.ingest = telemetry.NewIngestBatch(repository, app.metrics)
	app.history = telemetry.NewListHistory(repository)
	return app, nil
}

func WithClickHouse(cfg config.ClickHouse) Option {
	return func(ctx context.Context, app *Application) error {
		if app.clickHouse != nil {
			return errors.New("telemetry repository is already configured")
		}
		conn, err := ch.Open(&ch.Options{
			Addr:        []string{cfg.Address},
			Auth:        ch.Auth{Database: cfg.Database, Username: cfg.User, Password: cfg.Password},
			DialTimeout: connectTimeout,
		})
		if err != nil {
			return errors.New("invalid ClickHouse configuration")
		}
		startup, cancel := context.WithTimeout(ctx, connectTimeout)
		defer cancel()
		if err = conn.Ping(startup); err != nil {
			_ = conn.Close()
			return errors.New("ClickHouse connection failed")
		}
		app.clickHouse = conn
		return nil
	}
}

func (a *Application) Close() {
	if a.clickHouse != nil {
		_ = a.clickHouse.Close()
	}
}
