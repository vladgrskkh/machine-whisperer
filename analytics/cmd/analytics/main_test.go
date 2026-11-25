package main

import (
	"context"
	"log/slog"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vladgrskkh/machine-whisperer/analytics/internal/config"
)

func TestServeHTTPRequiresBothListeners(t *testing.T) {
	t.Parallel()
	business := availableAddress(t)
	occupied, err := new(net.ListenConfig).Listen(t.Context(), "tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = occupied.Close() })
	cfg := config.Config{Address: business, MetricsAddress: occupied.Addr().String()}

	err = serveHTTP(t.Context(), slog.New(slog.DiscardHandler), cfg, http.NotFoundHandler(), http.NotFoundHandler())
	require.ErrorContains(t, err, "serve metrics HTTP")
	listener, err := new(net.ListenConfig).Listen(t.Context(), "tcp", business)
	require.NoError(t, err, "the business listener must be released")
	_ = listener.Close()
}

func TestServeHTTPRejectsWildcardMetricsAddress(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	cfg := config.Config{Address: "127.0.0.1:0", MetricsAddress: "[::ffff:0.0.0.0]:0"}
	err := serveHTTP(ctx, slog.New(slog.DiscardHandler), cfg, http.NotFoundHandler(), http.NotFoundHandler())
	require.ErrorContains(t, err, "ANALYTICS_METRICS_ADDRESS cannot use a wildcard address")
}

func TestServeHTTPShutsDownBothListeners(t *testing.T) {
	t.Parallel()
	cfg := config.Config{Address: availableAddress(t), MetricsAddress: availableAddress(t)}
	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	probe := http.NewServeMux()
	probe.HandleFunc("GET /metrics", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	result := make(chan error, 1)
	go func() {
		result <- serveHTTP(ctx, slog.New(slog.DiscardHandler), cfg, http.NotFoundHandler(), probe)
	}()
	client := &http.Client{Timeout: time.Second}
	t.Cleanup(client.CloseIdleConnections)
	status := func(address string) int {
		res, err := client.Get("http://" + address + "/metrics")
		if err != nil {
			return 0
		}
		_ = res.Body.Close()
		return res.StatusCode
	}
	require.Eventually(t, func() bool {
		return status(cfg.MetricsAddress) == http.StatusNoContent && status(cfg.Address) == http.StatusNotFound
	}, 2*time.Second, 10*time.Millisecond)
	cancel()
	select {
	case err := <-result:
		require.NoError(t, err)
	case <-time.After(2 * time.Second):
		t.Fatal("both listeners must stop after cancellation")
	}
	assert.Zero(t, status(cfg.Address))
	assert.Zero(t, status(cfg.MetricsAddress))
}

func TestResolveMetricsAddress(t *testing.T) {
	t.Parallel()
	for _, address := range []string{"127.0.0.1:9091", "[::1]:9091", "localhost:9091"} {
		t.Run(address, func(t *testing.T) {
			t.Parallel()
			resolved, err := resolveMetricsAddress(address)
			require.NoError(t, err)
			host, port, err := net.SplitHostPort(resolved)
			require.NoError(t, err)
			assert.True(t, net.ParseIP(host).IsLoopback(), "expected a numeric loopback address in %q", resolved)
			assert.Equal(t, "9091", port)
		})
	}
	for _, address := range []string{
		"", ":9091", "127.0.0.1:", "0.0.0.0:9091", "[::]:9091", "[::ffff:0.0.0.0]:9091", "[::%lo0]:9091",
	} {
		t.Run(address, func(t *testing.T) {
			t.Parallel()
			resolved, err := resolveMetricsAddress(address)
			require.ErrorContains(t, err, "ANALYTICS_METRICS_ADDRESS")
			assert.Empty(t, resolved)
		})
	}
}

func availableAddress(t *testing.T) string {
	t.Helper()
	listener, err := new(net.ListenConfig).Listen(t.Context(), "tcp", "127.0.0.1:0")
	require.NoError(t, err)
	address := listener.Addr().String()
	require.NoError(t, listener.Close())
	return address
}
