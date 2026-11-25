package application

import (
	"context"

	ch "github.com/ClickHouse/clickhouse-go/v2"
)

func WithTestConnection(conn ch.Conn) Option {
	return func(_ context.Context, app *Application) error {
		app.clickHouse = conn
		return nil
	}
}
