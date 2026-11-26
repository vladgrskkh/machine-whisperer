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

	"github.com/vladgrskkh/machine-whisperer/api/internal/infrastructure/tracing"
)

func TestHTTPServerUsesStatusCodeAsErrorType(t *testing.T) {
	t.Parallel()
	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	t.Cleanup(func() { assert.NoError(t, provider.Shutdown(context.WithoutCancel(t.Context()))) })
	router := chi.NewRouter()
	router.Use(tracing.HTTPServer(provider, propagation.TraceContext{}, nil))
	router.Get("/test/{id}", func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "sensitive error", http.StatusInternalServerError)
	})
	finished := make(chan struct{}, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		router.ServeHTTP(w, r)
		finished <- struct{}{}
	}))
	t.Cleanup(server.Close)
	response, err := server.Client().Get(server.URL + "/test/sensitive-id")
	require.NoError(t, err)
	require.NoError(t, response.Body.Close())
	<-finished
	spans := recorder.Ended()
	require.Len(t, spans, 1)
	assert.Equal(t, codes.Error, spans[0].Status().Code)
	assert.Empty(t, spans[0].Status().Description)
	assert.Contains(t, spans[0].Attributes(), attribute.String("error.type", "500"))
}
