package metrics

import (
	"net/http"
	"time"

	"github.com/ClickHouse/clickhouse-go/v2/lib/driver"
	"github.com/felixge/httpsnoop"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/vladgrskkh/machine-whisperer/analytics/internal/infrastructure/clickhouse"
)

const (
	batchStored       = "stored"
	batchInvalid      = "invalid"
	batchTooLarge     = "too_large"
	batchUnauthorized = "unauthorized"
	batchStorageError = "storage_error"
)

type Metrics struct {
	registry   *prometheus.Registry
	http       httpMetrics
	batches    *prometheus.CounterVec
	storedRows prometheus.Counter
	operations *prometheus.CounterVec
	duration   *prometheus.HistogramVec
}

func New(stats func() driver.Stats) *Metrics {
	registry := prometheus.NewRegistry()
	m := &Metrics{
		registry: registry,
		http:     newHTTP(registry),
		batches: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "mw_telemetry_batches_total",
			Help: "Telemetry batch request outcomes.",
		}, []string{"outcome"}),
		storedRows: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "mw_telemetry_batch_events_stored_total",
			Help: "Telemetry batch event rows acknowledged by ClickHouse.",
		}),
		operations: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "mw_clickhouse_operations_total",
			Help: "ClickHouse repository operation outcomes.",
		}, []string{"operation", "outcome"}),
		duration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "mw_clickhouse_operation_duration_seconds",
			Help:    "ClickHouse repository operation duration in seconds.",
			Buckets: latencyBuckets(),
		}, []string{"operation"}),
	}
	registry.MustRegister(
		collectors.NewGoCollector(), collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
		newPoolMetrics(stats), m.batches, m.storedRows, m.operations, m.duration,
	)
	for _, outcome := range []string{batchStored, batchInvalid, batchTooLarge, batchUnauthorized, batchStorageError} {
		m.batches.WithLabelValues(outcome)
	}
	for _, operation := range []string{clickhouse.OperationInsertBatch, clickhouse.OperationListHistory} {
		m.duration.WithLabelValues(operation)
		for _, outcome := range []string{
			clickhouse.OutcomeSuccess, clickhouse.OutcomeTimeout, clickhouse.OutcomeCanceled, clickhouse.OutcomeError,
		} {
			m.operations.WithLabelValues(operation, outcome)
		}
	}
	return m
}

func (m *Metrics) Handler() http.Handler {
	handler := promhttp.HandlerFor(m.registry, promhttp.HandlerOpts{})
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/metrics" {
			http.NotFound(w, r)
			return
		}
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", http.MethodGet)
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		handler.ServeHTTP(w, r)
	})
}

func (m *Metrics) Batch(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		response := httpsnoop.CaptureMetrics(next, w, r)
		outcome := batchStorageError
		switch response.Code {
		case http.StatusNoContent:
			outcome = batchStored
		case http.StatusBadRequest:
			outcome = batchInvalid
		case http.StatusRequestEntityTooLarge:
			outcome = batchTooLarge
		case http.StatusUnauthorized:
			outcome = batchUnauthorized
		}
		m.batches.WithLabelValues(outcome).Inc()
	})
}

func (m *Metrics) RecordOperation(operation, outcome string, elapsed time.Duration, storedRows int) {
	m.operations.WithLabelValues(operation, outcome).Inc()
	m.duration.WithLabelValues(operation).Observe(elapsed.Seconds())
	m.storedRows.Add(float64(storedRows))
}
