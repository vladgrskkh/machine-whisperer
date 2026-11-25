package metrics

import (
	"context"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/vladgrskkh/machine-whisperer/api/internal/domain/telemetry"
)

type OutboxMeasurer interface {
	Snapshot(context.Context) (telemetry.OutboxSnapshot, error)
}

type snapshotMetrics struct {
	outbox     OutboxMeasurer
	errors     prometheus.Counter
	timestamp  *prometheus.Desc
	pending    *prometheus.Desc
	pendingAge *prometheus.Desc
	retried    *prometheus.Desc
	retriedAge *prometheus.Desc
}

func newSnapshotMetrics(outbox OutboxMeasurer) *snapshotMetrics {
	return &snapshotMetrics{
		outbox: outbox,
		errors: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "mw_outbox_snapshot_errors_total",
			Help: "Failed outbox snapshot reads.",
		}),
		timestamp: prometheus.NewDesc(
			"mw_outbox_snapshot_timestamp_seconds",
			"Unix timestamp of the outbox snapshot, or zero if unavailable.",
			nil, nil,
		),
		pending: prometheus.NewDesc("mw_outbox_pending_events", "Undelivered outbox events.", nil, nil),
		pendingAge: prometheus.NewDesc(
			"mw_outbox_oldest_pending_age_seconds", "Age of the oldest undelivered outbox event in seconds.", nil, nil,
		),
		retried: prometheus.NewDesc(
			"mw_outbox_retry_events", "Undelivered outbox events with a recorded send failure.", nil, nil,
		),
		retriedAge: prometheus.NewDesc(
			"mw_outbox_oldest_retry_age_seconds", "Age of the oldest undelivered event with a send failure in seconds.",
			nil, nil,
		),
	}
}

func (m *snapshotMetrics) Describe(descs chan<- *prometheus.Desc) {
	m.errors.Describe(descs)
	descs <- m.timestamp
	descs <- m.pending
	descs <- m.pendingAge
	descs <- m.retried
	descs <- m.retriedAge
}

func (m *snapshotMetrics) Collect(samples chan<- prometheus.Metric) {
	defer m.errors.Collect(samples)
	// Collect has no HTTP request context. The use case bounds the query.
	snapshot, err := m.outbox.Snapshot(context.Background())
	if err != nil {
		m.errors.Inc()
		samples <- prometheus.MustNewConstMetric(m.timestamp, prometheus.GaugeValue, 0)
		return
	}
	samples <- prometheus.MustNewConstMetric(m.timestamp, prometheus.GaugeValue, float64(snapshot.At.Unix()))
	samples <- prometheus.MustNewConstMetric(m.pending, prometheus.GaugeValue, float64(snapshot.Pending))
	samples <- prometheus.MustNewConstMetric(m.pendingAge, prometheus.GaugeValue, snapshot.OldestPendingAgeSeconds)
	samples <- prometheus.MustNewConstMetric(m.retried, prometheus.GaugeValue, float64(snapshot.Retried))
	samples <- prometheus.MustNewConstMetric(m.retriedAge, prometheus.GaugeValue, snapshot.OldestRetryAgeSeconds)
}
