//go:build integration

package integration_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vladgrskkh/machine-whisperer/api/internal/testutil"
)

//nolint:tparallel // The schema check must follow the child fixture's cleanup.
func TestPostgresFixturesShareServerAndIsolateSchemas(t *testing.T) {
	t.Parallel()
	pool := testutil.Postgres(t)
	ctx := t.Context()
	var schema string
	require.NoError(t, pool.QueryRow(ctx, "SELECT current_schema()").Scan(&schema))
	const insertUser = `INSERT INTO users (id, email, password_hash)
		VALUES ('b0833589-67a7-45f1-992a-0ab50b2d6b07', 'fixture@example.com', 'test hash')`
	_, err := pool.Exec(ctx, insertUser)
	require.NoError(t, err)

	var otherSchema string
	if !t.Run("second fixture", func(t *testing.T) {
		otherPool := testutil.Postgres(t)
		assert.Equal(t, pool.Config().ConnConfig.Port, otherPool.Config().ConnConfig.Port)
		require.NoError(t, otherPool.QueryRow(t.Context(), "SELECT current_schema()").Scan(&otherSchema))
		assert.NotEqual(t, schema, otherSchema)
		_, err := otherPool.Exec(t.Context(), insertUser)
		require.NoError(t, err)
	}) {
		t.FailNow()
	}

	var exists bool
	require.NoError(t, pool.QueryRow(ctx, "SELECT EXISTS (SELECT FROM pg_namespace WHERE nspname = $1)", otherSchema).
		Scan(&exists))
	assert.False(t, exists)
	var count int
	require.NoError(t, pool.QueryRow(ctx, "SELECT count(*) FROM users").Scan(&count))
	assert.Equal(t, 1, count)
}
