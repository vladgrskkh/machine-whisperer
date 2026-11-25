package metrics

import (
	"net/http"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/vladgrskkh/machine-whisperer/api/internal/domain/telemetry"
	"github.com/vladgrskkh/machine-whisperer/api/internal/infrastructure/telemetry/analytics"
	telemetryusecase "github.com/vladgrskkh/machine-whisperer/api/internal/usecase/telemetry"
)

type Metrics struct {
	registry         *prometheus.Registry
	http             httpMetrics
	outbox           outboxMetrics
	ingest           *prometheus.CounterVec
	detector         *prometheus.CounterVec
	alertTransitions *prometheus.CounterVec
}

func New(outbox OutboxMeasurer) *Metrics {
	registry := prometheus.NewRegistry()
	registry.MustRegister(
		collectors.NewGoCollector(),
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
		newSnapshotMetrics(outbox),
	)
	return &Metrics{
		registry: registry,
		http:     newHTTP(registry),
		outbox:   newOutbox(registry),
		ingest: counterOutcomes(
			registry,
			"mw_telemetry_ingest_requests_total",
			"Telemetry ingest use-case outcomes.",
			"outcome",
			string(telemetryusecase.IngestAcceptedNew),
			string(telemetryusecase.IngestAcceptedReplay),
			string(telemetryusecase.IngestInvalid),
			string(telemetryusecase.IngestMachineMissing),
			string(telemetryusecase.IngestConflict),
			string(telemetryusecase.IngestOutsideWindow),
			string(telemetryusecase.IngestOutOfOrder),
			string(telemetryusecase.IngestInternalError),
		),
		detector: counterOutcomes(
			registry,
			"mw_detector_observations_total",
			"Committed detector observation outcomes.",
			"outcome",
			string(telemetryusecase.DetectorEvaluated),
			string(telemetryusecase.DetectorNoBaseline),
			string(telemetryusecase.DetectorUnknownState),
		),
		alertTransitions: counterOutcomes(
			registry,
			"mw_alert_transitions_total",
			"Committed machine alert transitions.",
			"transition",
			string(telemetryusecase.AlertOpened),
			string(telemetryusecase.AlertRecovered),
			"acknowledged",
		),
	}
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

func (m *Metrics) RecordIngest(outcome telemetryusecase.IngestOutcome) {
	m.ingest.WithLabelValues(string(outcome)).Inc()
}

func (m *Metrics) RecordDetection(outcome telemetryusecase.DetectorOutcome) {
	m.detector.WithLabelValues(string(outcome)).Inc()
}

func (m *Metrics) RecordAlertTransition(transition telemetryusecase.AlertTransition) {
	m.alertTransitions.WithLabelValues(string(transition)).Inc()
}

func (m *Metrics) RecordAcknowledgement() {
	m.alertTransitions.WithLabelValues("acknowledged").Inc()
}

type outboxMetrics struct {
	claimed         prometheus.Counter
	delivered       prometheus.Counter
	retried         *prometheus.CounterVec
	sendAttempts    *prometheus.CounterVec
	sendDuration    *prometheus.HistogramVec
	operationErrors *prometheus.CounterVec
}

func newOutbox(registry *prometheus.Registry) outboxMetrics {
	reasons := []string{telemetry.OutboxAnalyticsHTTP, telemetry.OutboxAnalyticsTimeout,
		telemetry.OutboxAnalyticsTransport, telemetry.OutboxAnalyticsRequest}
	outcomes := append([]string{analytics.SendSuccess, analytics.SendCanceled}, reasons...)
	duration := prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "mw_outbox_send_duration_seconds",
		Help:    "Outbox batch HTTP send duration in seconds.",
		Buckets: latencyBuckets(),
	}, []string{"outcome"})
	registry.MustRegister(duration)
	for _, outcome := range outcomes {
		duration.WithLabelValues(outcome)
	}
	return outboxMetrics{
		claimed: count(registry, "mw_outbox_claimed_events_total", "Outbox events acquired under a lease."),
		delivered: count(
			registry,
			"mw_outbox_delivered_events_total",
			"Outbox events marked delivered under a valid lease.",
		),
		retried: counterOutcomes(registry, "mw_outbox_retried_events_total",
			"Outbox events rescheduled under a valid lease.", "reason", reasons...),
		sendAttempts: counterOutcomes(registry, "mw_outbox_send_attempts_total",
			"Outbound HTTP batch sends.", "outcome", outcomes...),
		sendDuration: duration,
		operationErrors: counterOutcomes(registry, "mw_outbox_operation_errors_total",
			"Failed outbox database operations.", "operation", telemetryusecase.OutboxOperationClaim,
			telemetryusecase.OutboxOperationAcknowledge, telemetryusecase.OutboxOperationReschedule,
			telemetryusecase.OutboxOperationPrune),
	}
}

func (m *Metrics) RecordClaimed(rows int) {
	m.outbox.claimed.Add(float64(rows))
}

func (m *Metrics) RecordDelivered(rows int64) {
	m.outbox.delivered.Add(float64(rows))
}

func (m *Metrics) RecordRetried(reason string, rows int64) {
	m.outbox.retried.WithLabelValues(reason).Add(float64(rows))
}

func (m *Metrics) RecordSend(outcome string, duration time.Duration) {
	m.outbox.sendAttempts.WithLabelValues(outcome).Inc()
	m.outbox.sendDuration.WithLabelValues(outcome).Observe(duration.Seconds())
}

func (m *Metrics) RecordOutboxError(operation string) {
	m.outbox.operationErrors.WithLabelValues(operation).Inc()
}

func count(registry *prometheus.Registry, name, help string) prometheus.Counter {
	counter := prometheus.NewCounter(prometheus.CounterOpts{Name: name, Help: help})
	registry.MustRegister(counter)
	return counter
}

func counterOutcomes(registry *prometheus.Registry, name, help, label string, values ...string) *prometheus.CounterVec {
	counter := prometheus.NewCounterVec(prometheus.CounterOpts{Name: name, Help: help}, []string{label})
	registry.MustRegister(counter)
	for _, value := range values {
		counter.WithLabelValues(value)
	}
	return counter
}
