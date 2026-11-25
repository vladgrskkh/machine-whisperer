package metrics

import (
	"sync/atomic"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/vladgrskkh/machine-whisperer/api/internal/domain/telemetry"
)

type poolMetrics struct {
	pool        *pgxpool.Pool
	connections *prometheus.Desc
}

func newPoolMetrics(pool *pgxpool.Pool) poolMetrics {
	return poolMetrics{
		pool: pool,
		connections: prometheus.NewDesc(
			"mw_postgres_pool_connections", "PostgreSQL pool connections by state.", []string{"state"}, nil,
		),
	}
}

func (m poolMetrics) Describe(descs chan<- *prometheus.Desc) {
	descs <- m.connections
}

func (m poolMetrics) Collect(samples chan<- prometheus.Metric) {
	stats := m.pool.Stat()
	samples <- prometheus.MustNewConstMetric(m.connections, prometheus.GaugeValue, float64(stats.AcquiredConns()), "acquired")
	samples <- prometheus.MustNewConstMetric(m.connections, prometheus.GaugeValue, float64(stats.IdleConns()), "idle")
	samples <- prometheus.MustNewConstMetric(
		m.connections, prometheus.GaugeValue, float64(stats.ConstructingConns()), "constructing",
	)
}

type snapshotMetrics struct {
	latest     atomic.Pointer[telemetry.OutboxSnapshot]
	timestamp  *prometheus.Desc
	pending    *prometheus.Desc
	pendingAge *prometheus.Desc
	retried    *prometheus.Desc
	retriedAge *prometheus.Desc
}

func newSnapshotMetrics() *snapshotMetrics {
	return &snapshotMetrics{
		timestamp: prometheus.NewDesc(
			"mw_outbox_snapshot_timestamp_seconds", "Unix timestamp of the last successful outbox snapshot.", nil, nil,
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
	descs <- m.timestamp
	descs <- m.pending
	descs <- m.pendingAge
	descs <- m.retried
	descs <- m.retriedAge
}

func (m *snapshotMetrics) Collect(samples chan<- prometheus.Metric) {
	snapshot := m.latest.Load()
	if snapshot == nil {
		samples <- prometheus.MustNewConstMetric(m.timestamp, prometheus.GaugeValue, 0)
		return
	}
	samples <- prometheus.MustNewConstMetric(m.timestamp, prometheus.GaugeValue, float64(snapshot.At.Unix()))
	samples <- prometheus.MustNewConstMetric(m.pending, prometheus.GaugeValue, float64(snapshot.Pending))
	samples <- prometheus.MustNewConstMetric(m.pendingAge, prometheus.GaugeValue, snapshot.OldestPendingAgeSeconds)
	samples <- prometheus.MustNewConstMetric(m.retried, prometheus.GaugeValue, float64(snapshot.Retried))
	samples <- prometheus.MustNewConstMetric(m.retriedAge, prometheus.GaugeValue, snapshot.OldestRetryAgeSeconds)
}
