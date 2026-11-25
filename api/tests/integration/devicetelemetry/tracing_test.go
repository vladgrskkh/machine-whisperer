//go:build integration

package devicetelemetry_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/propagation"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"

	"github.com/vladgrskkh/machine-whisperer/api/internal/application"
	"github.com/vladgrskkh/machine-whisperer/api/tests/integration/internal/testfixture"
)

func TestTelemetryTraceparentPersistsOnlyForOriginalSampledIngest(t *testing.T) {
	t.Parallel()
	recorder, tracing := recordTraces(t)
	config := testfixture.AuthConfig(t)
	config.RateLimits.IP.TrustedProxies = []netip.Prefix{netip.MustParsePrefix("127.0.0.1/32")}
	fixture := testfixture.SetupDeviceTelemetryWithConfig(t, config, tracing)

	eventID := uuid.NewString()
	body := observationJSON(eventID, fixture.Observed)
	post := func(body, traceparent string) {
		t.Helper()
		headers := testfixture.Bearer(fixture.Device.APIKey)
		headers.Set("Traceparent", traceparent)
		response := testfixture.AuthHTTPRequest(
			t,
			fixture.Server,
			http.MethodPost,
			deviceMeTelemetryPath,
			body,
			headers,
		)
		require.Equal(t, http.StatusAccepted, response.Status)
	}

	post(body, "00-12345678901234567890123456789012-1234567890123456-01")
	ingests := endedSpans(recorder, "telemetry.ingest")
	require.Len(t, ingests, 1)
	original := ingests[0].SpanContext()
	assert.Equal(t, "12345678901234567890123456789012", original.TraceID().String())
	assert.NotEqual(t, "1234567890123456", original.SpanID().String())
	var stored string
	var beforeHash, afterHash []byte
	const readOriginal = `SELECT o.traceparent,e.payload_hash FROM telemetry_outbox o
		JOIN telemetry_events e USING (device_id,event_id) WHERE o.event_id=$1`
	require.NoError(t, fixture.Pool.QueryRow(t.Context(), readOriginal, eventID).Scan(&stored, &beforeHash))
	want := fmt.Sprintf("00-%s-%s-01", original.TraceID(), original.SpanID())
	assert.Equal(t, want, stored)

	post(body, "00-22345678901234567890123456789012-1234567890123456-01")
	require.NoError(t, fixture.Pool.QueryRow(t.Context(), readOriginal, eventID).Scan(&stored, &afterHash))
	assert.Equal(t, want, stored)
	assert.Equal(t, beforeHash, afterHash)

	unsampledID := uuid.NewString()
	post(observationJSON(unsampledID, fixture.Observed), "00-32345678901234567890123456789012-1234567890123456-00")
	var unsampled *string
	require.NoError(t, fixture.Pool.QueryRow(t.Context(),
		"SELECT traceparent FROM telemetry_outbox WHERE event_id=$1", unsampledID).Scan(&unsampled))
	assert.Nil(t, unsampled)
	fixture.AssertStored(t, 2, 2, &fixture.Observed)
}

func TestOutboxDeliveryTraceLinksToHTTPIngestAcrossRetries(t *testing.T) {
	t.Parallel()
	recorder, tracing := recordTraces(t)
	propagator := propagation.TraceContext{}
	var sends atomic.Int64
	received := make(chan trace.SpanContext, 2)
	analytics := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload struct {
			Events []map[string]json.RawMessage `json:"events"`
		}
		if assert.NoError(t, json.NewDecoder(r.Body).Decode(&payload)) && assert.Len(t, payload.Events, 1) {
			assert.Len(t, payload.Events[0], 5)
			assert.NotContains(t, payload.Events[0], "traceparent")
		}
		if request := sends.Add(1); request <= 2 {
			ctx := propagator.Extract(r.Context(), propagation.HeaderCarrier(r.Header))
			received <- trace.SpanContextFromContext(ctx)
			if request == 1 {
				w.WriteHeader(http.StatusServiceUnavailable)
				return
			}
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(analytics.Close)
	config := testfixture.AuthConfig(t)
	config.Relay = application.RelayConfig{
		AnalyticsURL: analytics.URL, AnalyticsAPIKey: "test-key",
		HTTPTimeout: time.Second, BatchSize: 1000, PollInterval: 20 * time.Millisecond,
		Lease: time.Minute, RetryBase: time.Hour, RetryMax: time.Hour, Retention: 30 * 24 * time.Hour,
	}
	fixture := testfixture.SetupDeviceTelemetryWithConfig(t, config, tracing)
	eventID := uuid.NewString()
	response := testfixture.AuthHTTPRequest(t, fixture.Server, http.MethodPost, deviceMeTelemetryPath,
		observationJSON(eventID, fixture.Observed), testfixture.Bearer(fixture.Device.APIKey))
	require.Equal(t, http.StatusAccepted, response.Status)
	ingests := endedSpans(recorder, "telemetry.ingest")
	require.Len(t, ingests, 1)
	ingest := ingests[0]
	require.Eventually(t, func() bool {
		return len(endedSpans(recorder, "POST "+deviceMeTelemetryPath)) == 1
	}, time.Second, time.Millisecond)
	request := endedSpans(recorder, "POST "+deviceMeTelemetryPath)[0]
	assert.Equal(t, request.SpanContext(), ingest.Parent())

	stop := runOutboxWorker(t, fixture.App)
	require.Eventually(t, func() bool {
		return len(endedSpans(recorder, "outbox.deliver")) == 1
	}, 3*time.Second, 10*time.Millisecond)
	_, err := fixture.Pool.Exec(
		t.Context(),
		"UPDATE telemetry_outbox SET available_at=now() WHERE event_id=$1",
		eventID,
	)
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		return len(endedSpans(recorder, "outbox.deliver")) == 2
	}, 3*time.Second, 10*time.Millisecond)
	stop()

	attempts := endedSpans(recorder, "outbox.deliver")
	assert.Len(t, attempts, 2)
	assert.NotEqual(t, attempts[0].SpanContext().TraceID(), attempts[1].SpanContext().TraceID())
	assert.Equal(t, codes.Error, attempts[0].Status().Code)
	assert.Equal(t, codes.Unset, attempts[1].Status().Code)
	deliveries := endedSpans(recorder, "outbox.batch.deliver")
	require.Len(t, deliveries, 2)
	assert.Contains(t, deliveries[0].Attributes(), attribute.String("error.type", "analytics_http"))
	clientSpans := endedSpans(recorder, http.MethodPost)
	require.Len(t, clientSpans, 2)
	require.Len(t, received, 2)
	for index, attempt := range attempts {
		assert.False(t, attempt.Parent().IsValid())
		assert.NotEqual(t, ingest.SpanContext().TraceID(), attempt.SpanContext().TraceID())
		require.Len(t, attempt.Links(), 1)
		assert.Equal(t, ingest.SpanContext().WithRemote(true), attempt.Links()[0].SpanContext)
		assert.Equal(t, attempt.SpanContext(), deliveries[index].Parent())
		assert.Equal(t, deliveries[index].SpanContext(), clientSpans[index].Parent())
		assert.Equal(t, clientSpans[index].SpanContext().WithRemote(true), <-received)
	}
	assert.EqualValues(t, 2, sends.Load())
	var delivered bool
	var traceparent string
	require.NoError(t, fixture.Pool.QueryRow(t.Context(),
		"SELECT delivered_at IS NOT NULL,traceparent FROM telemetry_outbox WHERE event_id=$1", eventID).
		Scan(&delivered, &traceparent))
	assert.True(t, delivered)
	assert.Equal(
		t,
		fmt.Sprintf("00-%s-%s-01", ingest.SpanContext().TraceID(), ingest.SpanContext().SpanID()),
		traceparent,
	)
}

func recordTraces(t *testing.T) (*tracetest.SpanRecorder, application.Option) {
	t.Helper()
	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	t.Cleanup(func() { assert.NoError(t, provider.Shutdown(context.WithoutCancel(t.Context()))) })
	return recorder, application.WithTracing(provider, propagation.TraceContext{})
}

func endedSpans(recorder *tracetest.SpanRecorder, name string) []sdktrace.ReadOnlySpan {
	var matches []sdktrace.ReadOnlySpan
	for _, span := range recorder.Ended() {
		if span.Name() == name {
			matches = append(matches, span)
		}
	}
	return matches
}

func runOutboxWorker(t *testing.T, app *application.Application) func() {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		app.RunWorkers(ctx)
	}()
	stop := func() {
		cancel()
		select {
		case <-stopped:
		case <-time.After(time.Second):
			t.Error("outbox worker did not stop after cancellation")
		}
	}
	t.Cleanup(stop)
	return stop
}
