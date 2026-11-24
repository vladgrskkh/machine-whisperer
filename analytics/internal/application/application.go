package application

import (
	"context"
	"errors"
	"time"

	ch "github.com/ClickHouse/clickhouse-go/v2"

	"github.com/vladgrskkh/machine-whisperer/analytics/internal/config"
	"github.com/vladgrskkh/machine-whisperer/analytics/internal/infrastructure/clickhouse"
	"github.com/vladgrskkh/machine-whisperer/analytics/internal/usecase/telemetry"
)

const connectTimeout = 10 * time.Second

type Application struct {
	ingestKey  string
	clickHouse ch.Conn
	ingest     *telemetry.IngestBatch
	history    *telemetry.ListHistory
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
	if app.clickHouse == nil {
		return nil, errors.New("telemetry repository is required")
	}
	repository := clickhouse.NewRepository(app.clickHouse)
	app.ingest = telemetry.NewIngestBatch(repository)
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
