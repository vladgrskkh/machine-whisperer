package application_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vladgrskkh/machine-whisperer/api/internal/application"
	"github.com/vladgrskkh/machine-whisperer/api/internal/testutil"
)

func TestNewRejectsInvalidKeys(t *testing.T) {
	t.Parallel()
	handler, err := application.New(
		t.Context(),
		application.Config{Auth: application.AuthConfig{SigningKeyFile: "private-path-with-secret"}},
	)
	require.Error(t, err)
	assert.Nil(t, handler)
	assert.NotContains(t, err.Error(), "private-path-with-secret")
}

func TestNewPropagatesStartupCancellation(t *testing.T) {
	t.Parallel()
	id, signing, verifying, _ := testutil.AuthKeys(t)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	handler, err := application.New(
		ctx,
		application.Config{
			Auth: application.AuthConfig{SigningKeyID: id, SigningKeyFile: signing, VerifyKeysFile: verifying},
		},
	)
	require.Error(t, err)
	assert.Nil(t, handler)
}

func TestHealthzReportsProcessHealthWithoutCredentials(t *testing.T) {
	t.Parallel()
	id, signing, verifying, _ := testutil.AuthKeys(t)
	pool, err := pgxpool.New(t.Context(), "postgres://unused:unused@localhost/unused")
	require.NoError(t, err)
	t.Cleanup(pool.Close)
	app, err := application.New(t.Context(), application.Config{
		Auth: application.AuthConfig{SigningKeyID: id, SigningKeyFile: signing, VerifyKeysFile: verifying},
	}, application.WithPool(pool))
	require.NoError(t, err)
	t.Cleanup(app.Close)
	server := httptest.NewServer(app.Routes())
	t.Cleanup(server.Close)

	response, err := server.Client().Get(server.URL + "/healthz")
	require.NoError(t, err)
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, response.StatusCode)
	assert.Empty(t, body)
}
