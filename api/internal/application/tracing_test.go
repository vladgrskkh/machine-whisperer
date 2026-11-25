package application_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/propagation"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"

	"github.com/vladgrskkh/machine-whisperer/api/internal/application"
	"github.com/vladgrskkh/machine-whisperer/api/internal/handler/middleware"
	"github.com/vladgrskkh/machine-whisperer/api/internal/testutil"
)

func TestHTTPServerTrustsTraceContextFromProxySocket(t *testing.T) {
	t.Parallel()
	const remoteTraceID = "25fe23c1da524e45bc52f3361b334615"
	const remoteSpanID = "fc8d4c5b17cb2a1f"
	const validTraceparent = "00-" + remoteTraceID + "-" + remoteSpanID + "-01"
	const route = "/v1/machines/{machine_id}/telemetry"
	for _, test := range []struct {
		name         string
		proxy        string
		traceparent  string
		acceptParent bool
	}{
		{name: "trusted", proxy: "127.0.0.1/32", traceparent: validTraceparent, acceptParent: true},
		{name: "direct with spoofed forwarding header", proxy: "192.0.2.1/32", traceparent: validTraceparent},
		{name: "malformed header", proxy: "127.0.0.1/32", traceparent: "malformed"},
		{name: "missing header", proxy: "127.0.0.1/32"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			server, recorder, handled := tracingServer(t, netip.MustParsePrefix(test.proxy))
			request, err := http.NewRequestWithContext(t.Context(), http.MethodGet,
				server.URL+"/v1/machines/sensitive-machine/telemetry?cursor=sensitive-cursor", nil)
			require.NoError(t, err)
			request.Header.Set("X-Forwarded-For", "192.0.2.1")
			request.Header.Set("User-Agent", "sensitive-user-agent")
			request.Header.Set("Traceparent", test.traceparent)
			response, err := server.Client().Do(request)
			require.NoError(t, err)
			defer response.Body.Close()
			assert.Equal(t, http.StatusUnauthorized, response.StatusCode)

			<-handled
			spans := recorder.Ended()
			require.Len(t, spans, 1)
			span := spans[0]
			assert.Equal(t, "GET "+route, span.Name())
			assert.Equal(t, trace.SpanKindServer, span.SpanKind())
			assert.Equal(t, codes.Unset, span.Status().Code)
			assert.Empty(t, span.Links())
			if test.acceptParent {
				assert.Equal(t, remoteTraceID, span.SpanContext().TraceID().String())
				assert.Equal(t, remoteSpanID, span.Parent().SpanID().String())
				assert.True(t, span.Parent().IsRemote())
			} else {
				assert.False(t, span.Parent().IsValid())
				assert.NotEqual(t, remoteTraceID, span.SpanContext().TraceID().String())
			}
			assert.Equal(t, map[string]any{
				"http.request.method": "GET", "http.route": route, "http.response.status_code": int64(401),
			}, traceAttributes(span))
		})
	}
}

func TestHTTPServerSkipsProbesAndUsesMethodAsUnmatchedSpanName(t *testing.T) {
	t.Parallel()
	server, recorder, handled := tracingServer(t, netip.MustParsePrefix("127.0.0.1/32"))
	for _, path := range []string{
		"/healthz", "/metrics", "/private/path?cursor=sensitive", "/v1/auth/login/missing", "/v1/auth/login",
	} {
		response, err := server.Client().Get(server.URL + path)
		require.NoError(t, err)
		_, err = io.Copy(io.Discard, response.Body)
		require.NoError(t, err)
		require.NoError(t, response.Body.Close())
		<-handled
	}
	response, err := server.Client().Post(server.URL+"/v1/auth/login", "application/json", http.NoBody)
	require.NoError(t, err)
	require.NoError(t, response.Body.Close())
	<-handled

	spans := recorder.Ended()
	statuses := []int64{404, 404, 405}
	require.Len(t, spans, len(statuses)+1)
	for i, span := range spans[:len(statuses)] {
		assert.Equal(t, "GET", span.Name())
		assert.Equal(t, map[string]any{
			"http.request.method": "GET", "http.response.status_code": statuses[i],
		}, traceAttributes(span))
	}
	matched := spans[len(statuses)]
	assert.Equal(t, "POST /v1/auth/login", matched.Name())
	assert.Equal(t, map[string]any{
		"http.request.method": "POST", "http.route": "/v1/auth/login", "http.response.status_code": int64(400),
	}, traceAttributes(matched))
}

func tracingServer(t *testing.T, proxy netip.Prefix) (*httptest.Server, *tracetest.SpanRecorder, <-chan struct{}) {
	t.Helper()
	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	t.Cleanup(func() { assert.NoError(t, provider.Shutdown(context.WithoutCancel(t.Context()))) })
	id, signing, verifying, _ := testutil.AuthKeys(t)
	pool, err := pgxpool.New(t.Context(), "postgres://unused:unused@localhost/unused")
	require.NoError(t, err)
	t.Cleanup(pool.Close)
	app, err := application.New(t.Context(), application.Config{
		Auth:       application.AuthConfig{SigningKeyID: id, SigningKeyFile: signing, VerifyKeysFile: verifying},
		RateLimits: application.RateLimitsConfig{IP: middleware.RateLimitConfig{TrustedProxies: []netip.Prefix{proxy}}},
	}, application.WithPool(pool), application.WithTracing(provider, propagation.TraceContext{}))
	require.NoError(t, err)
	t.Cleanup(app.Close)
	handled := make(chan struct{}, 1)
	routes := app.Routes()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		routes.ServeHTTP(w, r)
		handled <- struct{}{}
	}))
	t.Cleanup(server.Close)
	return server, recorder, handled
}

func traceAttributes(span sdktrace.ReadOnlySpan) map[string]any {
	attributes := make(map[string]any)
	for _, attr := range span.Attributes() {
		attributes[string(attr.Key)] = attr.Value.AsInterface()
	}
	return attributes
}
