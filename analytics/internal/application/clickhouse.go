package application

import (
	"context"
	"errors"

	"github.com/vladgrskkh/machine-whisperer/analytics/internal/config"
	"github.com/vladgrskkh/machine-whisperer/analytics/internal/infrastructure/clickhouse"
)

func WithClickHouse(cfg config.ClickHouse) Option {
	return func(ctx context.Context, app *Application) error {
		if app.repository != nil {
			return errors.New("telemetry repository is already configured")
		}
		conn, err := clickhouse.Open(ctx, cfg)
		if err != nil {
			return err
		}
		app.repository = clickhouse.NewRepository(conn)
		app.closeClickHouse = conn.Close
		return nil
	}
}
