package application

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

const databaseStartupTimeout = 10 * time.Second

func NewPool(ctx context.Context, config Config) (*pgxpool.Pool, error) {
	startupCtx, cancel := context.WithTimeout(ctx, databaseStartupTimeout)
	defer cancel()
	pool, err := pgxpool.New(startupCtx, config.DatabaseURL)
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
