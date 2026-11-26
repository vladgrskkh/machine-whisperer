package tracing_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/propagation"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"

	"github.com/vladgrskkh/machine-whisperer/api/internal/infrastructure/tracing"
)

func TestHTTPServerUsesStatusCodeAsErrorType(t *testing.T) {
	t.Parallel()
	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	t.Cleanup(func() { assert.NoError(t, provider.Shutdown(context.WithoutCancel(t.Context()))) })
	router := chi.NewRouter()
	router.Use(tracing.HTTPServer(provider, propagation.TraceContext{}, nil))
	router.Route("/test", func(route chi.Router) {
		route.Get("/{id}", func(w http.ResponseWriter, r *http.Request) {
			assert.Equal(t, "Upgrade", r.Header.Get("Connection"))
			assert.Equal(t, "websocket", r.Header.Get("Upgrade"))
			trace.SpanFromContext(r.Context()).SetName("sensitive-route-override")
			started := recorder.Started()
			if assert.Len(t, started, 1) {
				assert.Equal(t, "GET /test/{id}", started[0].Name())
			}
			http.Error(w, "sensitive error", http.StatusInternalServerError)
		})
	})
	finished := make(chan struct{}, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		router.ServeHTTP(w, r)
		finished <- struct{}{}
	}))
	t.Cleanup(server.Close)
	request, err := http.NewRequestWithContext(t.Context(), http.MethodGet, server.URL+"/test/sensitive%2Fid", nil)
	require.NoError(t, err)
	request.Header.Set("Connection", "Upgrade")
	request.Header.Set("Upgrade", "websocket")
	response, err := server.Client().Do(request)
	require.NoError(t, err)
	assert.Equal(t, http.StatusInternalServerError, response.StatusCode)
	require.NoError(t, response.Body.Close())
	<-finished
	spans := recorder.Ended()
	require.Len(t, spans, 1)
	assert.Equal(t, codes.Error, spans[0].Status().Code)
	assert.Empty(t, spans[0].Status().Description)
	assert.Equal(t, "GET /test/{id}", spans[0].Name())
	assert.ElementsMatch(t, []attribute.KeyValue{
		attribute.String("http.request.method", "GET"),
		attribute.String("http.route", "/test/{id}"),
		attribute.Int("http.response.status_code", http.StatusInternalServerError),
		attribute.String("error.type", "500"),
	}, spans[0].Attributes())
}
