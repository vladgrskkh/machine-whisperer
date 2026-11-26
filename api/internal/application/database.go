package application

import (
	"context"
	"errors"
	"time"

	"github.com/exaring/otelpgx"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const databaseStartupTimeout = 10 * time.Second

// WithDatabase configures a pool created after all options, including tracing, have been applied.
func WithDatabase(config DatabaseConfig) Option {
	return func(_ context.Context, app *Application) error {
		if app.pool != nil || app.databaseConfig != nil {
			return errors.New("database pool is already configured")
		}
		app.databaseConfig = &config
		return nil
	}
}

// WithPool supplies a pool whose lifecycle and tracing configuration are managed by the caller.
func WithPool(pool *pgxpool.Pool) Option {
	return func(_ context.Context, app *Application) error {
		if pool == nil || app.pool != nil || app.databaseConfig != nil {
			return errors.New("invalid database pool configuration")
		}
		app.pool = pool
		return nil
	}
}

func (a *Application) setupDatabase(ctx context.Context) error {
	if a.pool != nil {
		return nil
	}
	if a.databaseConfig == nil {
		return errors.New("database pool is required")
	}
	tracer := otelpgx.NewTracer(otelpgx.WithTracerProvider(a.tracerProvider))
	pool, err := newPool(ctx, a.databaseConfig.URL, tracer)
	if err != nil {
		return err
	}
	a.pool = pool
	a.ownsPool = true
	return nil
}

func newPool(ctx context.Context, url string, tracer pgx.QueryTracer) (*pgxpool.Pool, error) {
	startupCtx, cancel := context.WithTimeout(ctx, databaseStartupTimeout)
	defer cancel()
	config, err := pgxpool.ParseConfig(url)
	if err != nil {
		return nil, errors.New("invalid database configuration")
	}
	config.ConnConfig.Tracer = tracer
	pool, err := pgxpool.NewWithConfig(startupCtx, config)
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
