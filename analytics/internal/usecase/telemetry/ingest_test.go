package telemetry_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"

	"github.com/vladgrskkh/machine-whisperer/analytics/internal/application"
	"github.com/vladgrskkh/machine-whisperer/analytics/internal/domain"
	"github.com/vladgrskkh/machine-whisperer/analytics/internal/infrastructure/metrics"
	"github.com/vladgrskkh/machine-whisperer/analytics/internal/usecase/telemetry"
)

func TestIngestBatchRecordsAcknowledgedRowsAndStorageErrors(t *testing.T) {
	t.Parallel()
	registry := metrics.New()
	spans := sdktrace.NewTracerProvider()
	tracer := spans.Tracer("test")
	ctx, parent := tracer.Start(t.Context(), "outbound request")
	defer parent.End()
	var logs bytes.Buffer
	logger := application.NewLogger(&logs, "test")
	storageErr := errors.New("private storage failure details")
	events := []domain.Event{{}, {}}
	failing := telemetry.NewIngestBatch(storageResult{err: storageErr}, registry, logger, tracer)
	require.ErrorIs(t, failing.Execute(ctx, events), storageErr)

	storing := telemetry.NewIngestBatch(storageResult{}, registry, logger, tracer)
	for range 2 {
		require.NoError(t, storing.Execute(ctx, events))
	}

	var entry struct {
		Message string `json:"msg"`
		Level   string `json:"level"`
		App     string `json:"app"`
		Env     string `json:"env"`
		TraceID string `json:"trace_id"`
		SpanID  string `json:"span_id"`
		Rows    int    `json:"rows"`
	}
	require.NoError(t, json.Unmarshal(logs.Bytes(), &entry))
	assert.Equal(t, "analytics telemetry batch store failed", entry.Message)
	assert.Equal(t, slog.LevelError.String(), entry.Level)
	assert.Equal(t, "analytics", entry.App)
	assert.Equal(t, "test", entry.Env)
	assert.Equal(t, parent.SpanContext().TraceID().String(), entry.TraceID)
	assert.Regexp(t, "^[a-f0-9]{16}$", entry.SpanID)
	assert.NotEqual(t, parent.SpanContext().SpanID().String(), entry.SpanID)
	assert.Equal(t, len(events), entry.Rows)
	assert.NotContains(t, logs.String(), storageErr.Error())

	response := httptest.NewRecorder()
	request := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/metrics", nil)
	registry.Handler().ServeHTTP(response, request)
	require.Equal(t, http.StatusOK, response.Code)
	body := response.Body.String()
	assert.Contains(t, body, "mw_telemetry_batches_total{outcome=\"stored\"} 2\n")
	assert.Contains(t, body, "mw_telemetry_batches_total{outcome=\"storage_error\"} 1\n")
	assert.Contains(t, body, "mw_telemetry_batch_events_stored_total 4\n")
}

type storageResult struct{ err error }

func (s storageResult) StoreBatch(context.Context, []domain.Event) error { return s.err }
