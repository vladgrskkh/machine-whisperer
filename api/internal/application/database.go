package application

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

const databaseStartupTimeout = 10 * time.Second

func WithDatabase(config DatabaseConfig) Option {
	return func(ctx context.Context, app *Application) error {
		if app.pool != nil {
			return errors.New("database pool is already configured")
		}
		pool, err := newPool(ctx, config.URL)
		if err != nil {
			return err
		}
		app.pool = pool
		app.ownsPool = true
		return nil
	}
}

// WithPool supplies a pool whose lifecycle is managed by the caller.
func WithPool(pool *pgxpool.Pool) Option {
	return func(_ context.Context, app *Application) error {
		if pool == nil || app.pool != nil {
			return errors.New("invalid database pool configuration")
		}
		app.pool = pool
		return nil
	}
}

func newPool(ctx context.Context, url string) (*pgxpool.Pool, error) {
	startupCtx, cancel := context.WithTimeout(ctx, databaseStartupTimeout)
	defer cancel()
	pool, err := pgxpool.New(startupCtx, url)
	if err != nil {
		return nil, errors.New("invalid database configuration")
	}
	if err = pool.Ping(startupCtx); err != nil {
		pool.Close()
		// Driver errors can include the connection string; do not log them at startup.
		return nil, errors.New("database connection failed")
	}
	return pool, nil
}
