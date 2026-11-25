//go:build integration

package testfixture

import (
	"context"
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
	body, status, err := getAPIMetrics(t.Context(), server)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, status)
	return body
}

func getAPIMetrics(ctx context.Context, server *httptest.Server) (string, int, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, server.URL+"/metrics", nil)
	if err != nil {
		return "", 0, err
	}
	response, err := server.Client().Do(request)
	if err != nil {
		return "", 0, err
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	return string(body), response.StatusCode, err
}

func WaitForAPIMetrics(t *testing.T, server *httptest.Server, sample string) string {
	t.Helper()
	const (
		wait = 3 * time.Second
		tick = 20 * time.Millisecond
	)
	require.EventuallyWithT(t, func(c *assert.CollectT) {
		body, status, err := getAPIMetrics(t.Context(), server)
		require.NoError(c, err)
		require.Equal(c, http.StatusOK, status)
		assert.Contains(c, body, sample)
	}, wait, tick)
	return APIMetrics(t, server)
}
