//go:build integration

package testutil

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
)

const (
	startupTimeout = 2 * time.Minute
	cleanupTimeout = 10 * time.Second
)

func Postgres(t *testing.T) *pgxpool.Pool {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), startupTimeout)
	defer cancel()
	container, err := postgres.Run(
		ctx,
		"postgres:18-alpine",
		postgres.WithDatabase("signup"),
		postgres.WithUsername("signup"),
		postgres.WithPassword("test-password"),
		postgres.BasicWaitStrategies(),
	)
	testcontainers.CleanupContainer(t, container)
	require.NoError(t, err)
	url, err := container.ConnectionString(ctx, "sslmode=disable")
	require.NoError(t, err)
	config, err := pgxpool.ParseConfig(url)
	require.NoError(t, err)
	admin, err := pgxpool.NewWithConfig(ctx, config)
	require.NoError(t, err)
	t.Cleanup(admin.Close)
	schema := pgx.Identifier{"test_" + uuid.NewString()}.Sanitize()
	_, err = admin.Exec(ctx, "CREATE SCHEMA "+schema)
	require.NoError(t, err)
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), cleanupTimeout)
		defer cleanupCancel()
		_, cleanupErr := admin.Exec(cleanupCtx, "DROP SCHEMA "+schema+" CASCADE")
		assertCleanup(t, cleanupErr)
	})
	config.ConnConfig.RuntimeParams["search_path"] = schema
	pool, err := pgxpool.NewWithConfig(ctx, config)
	require.NoError(t, err)
	t.Cleanup(pool.Close)
	db := stdlib.OpenDB(*config.ConnConfig)
	defer db.Close()
	_, filename, _, ok := runtime.Caller(0)
	require.True(t, ok)
	provider, err := goose.NewProvider(
		goose.DialectPostgres,
		db,
		os.DirFS(filepath.Join(filepath.Dir(filename), "../../migrations")),
	)
	require.NoError(t, err)
	_, err = provider.Up(ctx)
	require.NoError(t, err)
	return pool
}

func assertCleanup(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Errorf("clean test schema: %v", err)
	}
}
