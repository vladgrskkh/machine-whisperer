package middleware_test

import (
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vladgrskkh/machine-whisperer/api/internal/handler/middleware"
)

func TestRateLimit(t *testing.T) {
	t.Parallel()
	var mu sync.Mutex
	now := time.Date(2026, time.November, 10, 0, 0, 0, 0, time.UTC)
	handler := middleware.RateLimit(
		middleware.RateLimitConfig{Now: func() time.Time { mu.Lock(); defer mu.Unlock(); return now }},
	)(
		http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }),
	)
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	for i := range 31 {
		req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, server.URL, nil)
		require.NoError(t, err)
		req.Header.Set("X-Forwarded-For", netip.AddrFrom4([4]byte{10, 0, 0, byte(i)}).String())
		response, err := server.Client().Do(req)
		require.NoError(t, err)
		response.Body.Close()
		expected := http.StatusNoContent
		if i == 30 {
			expected = http.StatusTooManyRequests
			assert.NotEmpty(t, response.Header.Get("Retry-After"))
		}
		assert.Equal(t, expected, response.StatusCode)
	}
	mu.Lock()
	now = now.Add(time.Minute)
	mu.Unlock()
	response, err := server.Client().Post(server.URL, "application/json", nil)
	require.NoError(t, err)
	response.Body.Close()
	assert.Equal(t, http.StatusNoContent, response.StatusCode)
}

func TestTrustedProxyAndCapacity(t *testing.T) {
	t.Parallel()
	handler := middleware.RateLimit(
		middleware.RateLimitConfig{
			Limit:          1,
			MaxClients:     2,
			TrustedProxies: []netip.Prefix{netip.MustParsePrefix("127.0.0.0/8"), netip.MustParsePrefix("::1/128")},
		},
	)(
		http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }),
	)
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	for _, tc := range []struct {
		forwarded string
		want      int
	}{
		{"10.0.0.1", 204}, {"10.0.0.1", 429}, {"10.0.0.2", 204}, {"10.0.0.3", 429},
	} {
		req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, server.URL, nil)
		require.NoError(t, err)
		req.Header.Set("X-Forwarded-For", tc.forwarded)
		response, err := server.Client().Do(req)
		require.NoError(t, err)
		response.Body.Close()
		assert.Equal(t, tc.want, response.StatusCode)
	}
}

func TestTrustedProxyRejectsSpoofedChain(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(middleware.RateLimit(middleware.RateLimitConfig{
		Limit: 1, TrustedProxies: []netip.Prefix{netip.MustParsePrefix("127.0.0.0/8")},
	})(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })))
	t.Cleanup(server.Close)
	for _, tc := range []struct {
		forwarded string
		want      int
	}{
		{"192.0.2.1, 198.51.100.1", 204},
		{"192.0.2.2, 198.51.100.1", 429},
		{"invalid, 198.51.100.1", 429},
		{"192.0.2.3, 198.51.100.2, 127.0.0.2", 204},
		{"192.0.2.4, 198.51.100.2, 127.0.0.3", 429},
		{"invalid, 198.51.100.3", 204},
		{"invalid, 198.51.100.3", 429},
		{"198.51.100.4, invalid", 204},
		{"198.51.100.5, invalid", 429},
	} {
		request, err := http.NewRequestWithContext(t.Context(), http.MethodPost, server.URL, nil)
		require.NoError(t, err)
		request.Header.Set("X-Forwarded-For", tc.forwarded)
		response, err := server.Client().Do(request)
		require.NoError(t, err)
		response.Body.Close()
		assert.Equal(t, tc.want, response.StatusCode, tc.forwarded)
	}
}

func TestRateLimitCapacityRecovers(t *testing.T) {
	t.Parallel()
	var mu sync.Mutex
	now := time.Date(2026, time.November, 10, 0, 0, 0, 0, time.UTC)
	server := httptest.NewServer(middleware.RateLimit(middleware.RateLimitConfig{
		Limit: 1, MaxClients: 1, TrustedProxies: []netip.Prefix{netip.MustParsePrefix("127.0.0.0/8")},
		Now: func() time.Time { mu.Lock(); defer mu.Unlock(); return now },
	})(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })))
	t.Cleanup(server.Close)
	for _, tc := range []struct {
		ip      string
		advance bool
		want    int
	}{
		{"192.0.2.1", false, 204}, {"192.0.2.2", false, 429}, {"192.0.2.1", false, 429}, {"192.0.2.2", true, 204},
	} {
		if tc.advance {
			mu.Lock()
			now = now.Add(time.Minute)
			mu.Unlock()
		}
		request, err := http.NewRequestWithContext(t.Context(), http.MethodPost, server.URL, nil)
		require.NoError(t, err)
		request.Header.Set("X-Forwarded-For", tc.ip)
		response, err := server.Client().Do(request)
		require.NoError(t, err)
		response.Body.Close()
		assert.Equal(t, tc.want, response.StatusCode)
	}
}

func TestRateLimitConcurrentRequests(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(
		middleware.RateLimit(
			middleware.RateLimitConfig{},
		)(
			http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }),
		),
	)
	t.Cleanup(server.Close)
	statuses := make(chan int, 60)
	var wg sync.WaitGroup
	for range 60 {
		wg.Go(func() {
			response, err := server.Client().Get(server.URL)
			if !assert.NoError(t, err) {
				return
			}
			response.Body.Close()
			statuses <- response.StatusCode
		})
	}
	wg.Wait()
	close(statuses)
	allowed, blocked := 0, 0
	for status := range statuses {
		switch status {
		case http.StatusNoContent:
			allowed++
		case http.StatusTooManyRequests:
			blocked++
		default:
			t.Errorf("unexpected status: %d", status)
		}
	}
	assert.Equal(t, 30, allowed)
	assert.Equal(t, 30, blocked)
}

func TestTrustedProxyRepeatedForwardedHeaders(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(middleware.RateLimit(middleware.RateLimitConfig{
		Limit: 1, TrustedProxies: []netip.Prefix{netip.MustParsePrefix("127.0.0.0/8")},
	})(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })))
	t.Cleanup(server.Close)
	for i, spoofed := range []string{"192.0.2.1", "192.0.2.2"} {
		request, err := http.NewRequestWithContext(t.Context(), http.MethodPost, server.URL, nil)
		require.NoError(t, err)
		request.Header.Add("X-Forwarded-For", spoofed)
		request.Header.Add("X-Forwarded-For", "198.51.100.1")
		response, err := server.Client().Do(request)
		require.NoError(t, err)
		response.Body.Close()
		expected := http.StatusNoContent
		if i == 1 {
			expected = http.StatusTooManyRequests
		}
		assert.Equal(t, expected, response.StatusCode)
	}
}

func TestTrustedProxyOversizedPrefixKeepsClientIdentity(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(middleware.RateLimit(middleware.RateLimitConfig{
		Limit:          1,
		TrustedProxies: []netip.Prefix{netip.MustParsePrefix("127.0.0.0/8"), netip.MustParsePrefix("::1/128")},
		Now:            func() time.Time { return time.Date(2026, time.November, 10, 0, 0, 0, 0, time.UTC) },
	})(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })))
	t.Cleanup(server.Close)
	for i, fields := range [][]string{
		{"198.51.100.1"},
		{strings.Repeat("x", 4096) + ", 198.51.100.1"},
		{strings.Repeat("x,", 4096) + "198.51.100.1"},
		{strings.Repeat("x", 8192), "198.51.100.1"},
		{strings.Repeat("x", 8192) + ", 198.51.100.1, 127.0.0.2"},
	} {
		request, err := http.NewRequestWithContext(t.Context(), http.MethodPost, server.URL, nil)
		require.NoError(t, err)
		for _, field := range fields {
			request.Header.Add("X-Forwarded-For", field)
		}
		response, err := server.Client().Do(request)
		require.NoError(t, err)
		response.Body.Close()
		expected := http.StatusTooManyRequests
		if i == 0 {
			expected = http.StatusNoContent
		}
		assert.Equal(t, expected, response.StatusCode, "request %d", i+1)
	}
}
