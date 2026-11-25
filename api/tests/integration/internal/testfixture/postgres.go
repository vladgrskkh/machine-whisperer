//go:build integration

package testfixture

import (
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func RequirePGCode(t *testing.T, err error, code string) {
	t.Helper()
	var pgErr *pgconn.PgError
	require.ErrorAs(t, err, &pgErr)
	assert.Equal(t, code, pgErr.Code)
}
