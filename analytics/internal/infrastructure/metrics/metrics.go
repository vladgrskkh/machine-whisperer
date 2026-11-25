package metrics

import (
	"net/http"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/vladgrskkh/machine-whisperer/analytics/internal/infrastructure/clickhouse"
)

const (
	batchStored       = "stored"
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

func New() *Metrics {
	registry := prometheus.NewRegistry()
	m := &Metrics{
		registry: registry,
		http:     newHTTP(registry),
		batches: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "mw_telemetry_batches_total",
			Help: "Telemetry batch storage outcomes.",
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
		m.batches, m.storedRows, m.operations, m.duration,
	)
	for _, outcome := range []string{batchStored, batchStorageError} {
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

func (m *Metrics) RecordStoredBatch(events int) {
	m.batches.WithLabelValues(batchStored).Inc()
	m.storedRows.Add(float64(events))
}

func (m *Metrics) RecordBatchStorageError() {
	m.batches.WithLabelValues(batchStorageError).Inc()
}

func (m *Metrics) RecordOperation(operation, outcome string, elapsed time.Duration) {
	m.operations.WithLabelValues(operation, outcome).Inc()
	m.duration.WithLabelValues(operation).Observe(elapsed.Seconds())
}
