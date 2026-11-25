package application_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/propagation"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"

	"github.com/vladgrskkh/machine-whisperer/analytics/internal/application"
)

func TestHTTPServerContinuesAPITraceAndKeepsBearerProtection(t *testing.T) {
	t.Parallel()
	const remoteTraceID = "25fe23c1da524e45bc52f3361b334615"
	const remoteSpanID = "fc8d4c5b17cb2a1f"
	const route = "/internal/v1/machines/{machine_id}/telemetry"
	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	t.Cleanup(func() { assert.NoError(t, provider.Shutdown(context.WithoutCancel(t.Context()))) })
	app := &application.Application{}
	require.NoError(t, application.WithTracing(provider, propagation.TraceContext{})(t.Context(), app))
	handled := make(chan struct{}, 1)
	routes := app.Routes()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		routes.ServeHTTP(w, r)
		handled <- struct{}{}
	}))
	t.Cleanup(server.Close)
	for _, path := range []string{"/healthz", "/metrics", "/internal/v1/machines/sensitive-machine/telemetry?cursor=sensitive"} {
		request, err := http.NewRequestWithContext(t.Context(), http.MethodGet, server.URL+path, nil)
		require.NoError(t, err)
		request.Header.Set("Traceparent", "00-"+remoteTraceID+"-"+remoteSpanID+"-01")
		request.Header.Set("Baggage", "key=sensitive-baggage")
		response, err := server.Client().Do(request)
		require.NoError(t, err)
		require.NoError(t, response.Body.Close())
		<-handled
		if path != "/healthz" && path != "/metrics" {
			assert.Equal(t, http.StatusUnauthorized, response.StatusCode)
		}
	}
	spans := recorder.Ended()
	require.Len(t, spans, 1)
	span := spans[0]
	assert.Equal(t, "GET "+route, span.Name())
	assert.Equal(t, trace.SpanKindServer, span.SpanKind())
	assert.Equal(t, codes.Unset, span.Status().Code)
	assert.Equal(t, remoteTraceID, span.SpanContext().TraceID().String())
	assert.Equal(t, remoteSpanID, span.Parent().SpanID().String())
	assert.True(t, span.Parent().IsRemote())
	attributes := make(map[string]any)
	for _, attr := range span.Attributes() {
		attributes[string(attr.Key)] = attr.Value.AsInterface()
	}
	assert.Equal(t, map[string]any{
		"http.request.method": "GET", "http.route": route, "http.response.status_code": int64(401),
	}, attributes)
}
