package analytics_test

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/propagation"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"

	"github.com/vladgrskkh/machine-whisperer/api/internal/domain/telemetry"
	"github.com/vladgrskkh/machine-whisperer/api/internal/infrastructure/metrics"
	"github.com/vladgrskkh/machine-whisperer/api/internal/infrastructure/telemetry/analytics"
	"github.com/vladgrskkh/machine-whisperer/api/internal/infrastructure/tracing"
)

func TestClientsPropagateChildSpansAndKeepSensitiveFieldsOut(t *testing.T) {
	t.Parallel()
	propagator := propagation.TraceContext{}
	got := make(chan trace.SpanContext, 2)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got <- trace.SpanContextFromContext(propagator.Extract(r.Context(), propagation.HeaderCarrier(r.Header)))
		if r.Method == http.MethodGet {
			_, _ = w.Write([]byte(`{"items":[]}`))
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(server.Close)

	provider, recorder := tracingProvider(t)
	transport := tracing.HTTPTransport(provider, propagator)
	ctx, parent := provider.Tracer("test").Start(t.Context(), "parent")
	defer parent.End()
	history := analytics.NewHistoryClient(server.URL, "sensitive-key", transport)
	_, err := history.List(ctx, "sensitive-machine", 1, "sensitive-cursor")
	require.NoError(t, err)
	batch := analytics.NewHTTPClient(server.URL, "sensitive-key", time.Second, metrics.New(nil), transport)
	require.NoError(t, batch.Send(ctx, nil))

	spans := recorder.Ended()
	require.Len(t, spans, 2)
	for index, method := range []string{http.MethodGet, http.MethodPost} {
		span := spans[index]
		remote := <-got
		assert.Equal(t, method, span.Name())
		assert.Equal(t, trace.SpanKindClient, span.SpanKind())
		assert.Equal(t, parent.SpanContext().SpanID(), span.Parent().SpanID())
		assert.Equal(t, parent.SpanContext().TraceID(), remote.TraceID())
		assert.Equal(t, span.SpanContext().SpanID(), remote.SpanID())
		assert.True(t, remote.IsRemote())
		assert.Equal(t, codes.Unset, span.Status().Code)
		assert.Equal(t, map[string]any{
			"http.request.method": method, "http.response.status_code": [...]int64{200, 204}[index],
		}, spanAttributes(span))
	}
}

func TestClientTransportFailureHasBoundedErrorClass(t *testing.T) {
	t.Parallel()
	listener, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	require.NoError(t, err)
	url := "http://" + listener.Addr().String()
	require.NoError(t, listener.Close())
	provider, recorder := tracingProvider(t)
	client := analytics.NewHistoryClient(url, "key", tracing.HTTPTransport(provider, propagation.TraceContext{}))
	_, err = client.List(t.Context(), "sensitive-machine", 1, "sensitive-cursor")
	require.ErrorIs(t, err, telemetry.ErrHistoryUnavailable)
	spans := recorder.Ended()
	require.Len(t, spans, 1)
	assert.Equal(t, codes.Error, spans[0].Status().Code)
	assert.Empty(t, spans[0].Status().Description)
	assert.Empty(t, spans[0].Events())
	assert.Equal(t, map[string]any{
		"http.request.method": "GET", "error.type": "network_error",
	}, spanAttributes(spans[0]))
}

func TestTracedClientsCancelInFlightRequests(t *testing.T) {
	t.Parallel()
	for _, method := range []string{http.MethodGet, http.MethodPost} {
		t.Run(method, func(t *testing.T) {
			t.Parallel()
			started := make(chan struct{})
			serverCanceled := make(chan error, 1)
			server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
				_, _ = io.Copy(io.Discard, r.Body)
				close(started)
				<-r.Context().Done()
				serverCanceled <- r.Context().Err()
			}))
			t.Cleanup(server.Close)
			provider, recorder := tracingProvider(t)
			transport := tracing.HTTPTransport(provider, propagation.TraceContext{})
			ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
			defer cancel()
			clientResult := make(chan error, 1)
			go func() {
				if method == http.MethodGet {
					client := analytics.NewHistoryClient(server.URL, "key", transport)
					_, err := client.List(ctx, "machine", 1, "")
					clientResult <- err
				} else {
					client := analytics.NewHTTPClient(server.URL, "key", 5*time.Second, metrics.New(nil), transport)
					clientResult <- client.Send(ctx, nil)
				}
			}()
			select {
			case <-started:
			case <-ctx.Done():
				t.Fatal("request did not reach the server")
			}
			cancel()
			select {
			case err := <-serverCanceled:
				assert.ErrorIs(t, err, context.Canceled)
			case <-time.After(2 * time.Second):
				t.Fatal("server request context remained active after canceling client")
			}
			select {
			case err := <-clientResult:
				if method == http.MethodGet {
					assert.ErrorIs(t, err, telemetry.ErrHistoryUnavailable)
				} else {
					assert.ErrorIs(t, err, telemetry.ErrAnalyticsTransport)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("client request remained active after cancellation")
			}
			spans := recorder.Ended()
			require.Len(t, spans, 1)
			assert.Equal(t, method, spans[0].Name())
			assert.Empty(t, spans[0].Events())
			assert.Empty(t, spans[0].Status().Description)
		})
	}
}

func tracingProvider(t *testing.T) (*sdktrace.TracerProvider, *tracetest.SpanRecorder) {
	t.Helper()
	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	t.Cleanup(func() { assert.NoError(t, provider.Shutdown(context.WithoutCancel(t.Context()))) })
	return provider, recorder
}

func spanAttributes(span sdktrace.ReadOnlySpan) map[string]any {
	attributes := make(map[string]any)
	for _, attr := range span.Attributes() {
		attributes[string(attr.Key)] = attr.Value.AsInterface()
	}
	return attributes
}
