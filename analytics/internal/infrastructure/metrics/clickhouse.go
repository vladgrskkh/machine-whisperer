package metrics

import (
	"github.com/ClickHouse/clickhouse-go/v2/lib/driver"
	"github.com/prometheus/client_golang/prometheus"
)

type poolMetrics struct {
	stats       func() driver.Stats
	connections *prometheus.Desc
}

func newPoolMetrics(stats func() driver.Stats) poolMetrics {
	return poolMetrics{
		stats: stats,
		connections: prometheus.NewDesc(
			"mw_clickhouse_pool_connections", "Native ClickHouse driver pool connections by state.",
			[]string{"state"}, nil,
		),
	}
}

func (m poolMetrics) Describe(descs chan<- *prometheus.Desc) {
	descs <- m.connections
}

func (m poolMetrics) Collect(samples chan<- prometheus.Metric) {
	stats := m.stats()
	samples <- prometheus.MustNewConstMetric(m.connections, prometheus.GaugeValue, float64(stats.Open), "open")
	samples <- prometheus.MustNewConstMetric(m.connections, prometheus.GaugeValue, float64(stats.Idle), "idle")
}
