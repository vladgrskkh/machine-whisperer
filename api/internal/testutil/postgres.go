//go:build integration

package testutil

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"sync"
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

// PostgresSuite starts a shared server on first use. Its zero value is ready to use.
type PostgresSuite struct {
	startOnce sync.Once
	startErr  error
	container *postgres.PostgresContainer
	admin     *pgxpool.Pool
}

func RunWithPostgres(m *testing.M, suites ...*PostgresSuite) int {
	code := m.Run()
	for _, suite := range suites {
		if err := suite.close(); err != nil {
			fmt.Fprintf(os.Stderr, "clean up integration PostgreSQL: %v\n", err)
			if code == 0 {
				code = 1
			}
		}
	}
	return code
}

func (s *PostgresSuite) start() error {
	ctx, cancel := context.WithTimeout(context.Background(), startupTimeout)
	defer cancel()
	var err error
	s.container, err = postgres.Run(
		ctx,
		"postgres:18-alpine",
		postgres.WithDatabase("signup"),
		postgres.WithUsername("signup"),
		postgres.WithPassword("test-password"),
		postgres.BasicWaitStrategies(),
	)
	if err != nil {
		return fmt.Errorf("start integration PostgreSQL: %w", err)
	}
	dsn, err := s.container.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		return fmt.Errorf("get integration PostgreSQL URL: %w", err)
	}
	s.admin, err = pgxpool.New(ctx, dsn)
	return err
}

func (s *PostgresSuite) close() error {
	if s.admin != nil {
		s.admin.Close()
	}
	if s.container == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), cleanupTimeout)
	defer cancel()
	return testcontainers.TerminateContainer(s.container, testcontainers.StopContext(ctx))
}

func (s *PostgresSuite) Pool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	s.startOnce.Do(func() { s.startErr = s.start() })
	require.NoError(t, s.startErr)
	ctx, cancel := context.WithTimeout(t.Context(), startupTimeout)
	defer cancel()
	schema := pgx.Identifier{"test_" + uuid.NewString()}.Sanitize()
	_, err := s.admin.Exec(ctx, "CREATE SCHEMA "+schema)
	require.NoError(t, err)
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), cleanupTimeout)
		defer cleanupCancel()
		_, cleanupErr := s.admin.Exec(cleanupCtx, "DROP SCHEMA "+schema+" CASCADE")
		assertCleanup(t, cleanupErr)
	})
	dsn, err := url.Parse(s.admin.Config().ConnString())
	require.NoError(t, err)
	query := dsn.Query()
	query.Set("search_path", schema)
	dsn.RawQuery = query.Encode()
	pool, err := pgxpool.New(ctx, dsn.String())
	require.NoError(t, err)
	t.Cleanup(pool.Close)
	_, err = PostgresMigrations(t, pool).Up(ctx)
	require.NoError(t, err)
	return pool
}

func PostgresMigrations(t *testing.T, pool *pgxpool.Pool) *goose.Provider {
	t.Helper()
	db := stdlib.OpenDBFromPool(pool)
	t.Cleanup(func() { assertCleanup(t, db.Close()) })
	_, filename, _, ok := runtime.Caller(0)
	require.True(t, ok)
	provider, err := goose.NewProvider(
		goose.DialectPostgres,
		db,
		os.DirFS(filepath.Join(filepath.Dir(filename), "../../migrations")),
	)
	require.NoError(t, err)
	return provider
}

func assertCleanup(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Errorf("clean test schema: %v", err)
	}
}
