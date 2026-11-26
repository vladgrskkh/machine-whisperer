package telemetry_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/trace/noop"

	"github.com/vladgrskkh/machine-whisperer/analytics/internal/domain"
	"github.com/vladgrskkh/machine-whisperer/analytics/internal/infrastructure/metrics"
	"github.com/vladgrskkh/machine-whisperer/analytics/internal/usecase/telemetry"
)

func TestIngestBatchRecordsAcknowledgedRowsAndStorageErrors(t *testing.T) {
	t.Parallel()
	registry := metrics.New()
	tracer := noop.NewTracerProvider().Tracer("test")
	storageErr := errors.New("insert failed")
	events := []domain.Event{{}, {}}
	failing := telemetry.NewIngestBatch(storageResult{err: storageErr}, registry, tracer)
	require.ErrorIs(t, failing.Execute(t.Context(), events), storageErr)

	storing := telemetry.NewIngestBatch(storageResult{}, registry, tracer)
	for range 2 {
		require.NoError(t, storing.Execute(t.Context(), events))
	}

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
