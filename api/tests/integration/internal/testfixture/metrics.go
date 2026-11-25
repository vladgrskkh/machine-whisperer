//go:build integration

package testfixture

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vladgrskkh/machine-whisperer/api/internal/application"
)

func NewAPIMetricsServer(t *testing.T, app *application.Application) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(app.MetricsHandler())
	t.Cleanup(server.Close)
	return server
}

func APIMetrics(t *testing.T, server *httptest.Server) string {
	t.Helper()
	body, status, err := getAPIMetrics(server)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, status)
	return body
}

func getAPIMetrics(server *httptest.Server) (string, int, error) {
	response, err := server.Client().Get(server.URL + "/metrics")
	if err != nil {
		return "", 0, err
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	return string(body), response.StatusCode, err
}

func WaitForAPIMetrics(t *testing.T, server *httptest.Server, sample string) string {
	t.Helper()
	require.EventuallyWithT(t, func(c *assert.CollectT) {
		body, status, err := getAPIMetrics(server)
		require.NoError(c, err)
		require.Equal(c, http.StatusOK, status)
		assert.Contains(c, body, sample)
	}, 3*time.Second, 20*time.Millisecond)
	return APIMetrics(t, server)
}
