package application_test

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vladgrskkh/machine-whisperer/analytics/internal/application"
)

func TestHealthz(t *testing.T) {
	app := &application.Application{}
	server := httptest.NewServer(app.Routes())
	t.Cleanup(server.Close)

	resp, err := server.Client().Get(server.URL + "/healthz")
	require.NoError(t, err)
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Empty(t, body)
}
