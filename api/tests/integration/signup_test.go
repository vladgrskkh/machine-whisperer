//go:build integration

package integration_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	handler "github.com/vladgrskkh/machine-whisperer/api/internal/handler/auth"
	"github.com/vladgrskkh/machine-whisperer/api/internal/infrastructure/auth/password"
	"github.com/vladgrskkh/machine-whisperer/api/internal/infrastructure/user/postgres"
	"github.com/vladgrskkh/machine-whisperer/api/internal/testutil"
	"github.com/vladgrskkh/machine-whisperer/api/internal/usecase/auth"
)

func TestSignupStoresExactEscapedUnicodePassword(t *testing.T) {
	t.Parallel()
	pool := testutil.Postgres(t)
	hasher := password.Argon2id{}
	server := httptest.NewServer(handler.New(auth.New(postgres.New(pool), hasher)))
	t.Cleanup(server.Close)
	response, err := server.Client().Post(server.URL, "application/json", strings.NewReader(
		`{"email":"unicode@example.com","password":"  exact\ud83d\ude00 \ufffd \\ud800 password  "}`,
	))
	require.NoError(t, err)
	defer response.Body.Close()
	require.Equal(t, http.StatusCreated, response.StatusCode)
	var hash string
	require.NoError(t, pool.QueryRow(t.Context(),
		"SELECT password_hash FROM users WHERE email=$1", "unicode@example.com").Scan(&hash))
	matches, err := hasher.Verify(t.Context(), "  exact\U0001f600 \ufffd \\ud800 password  ", hash)
	require.NoError(t, err)
	assert.True(t, matches)
}
