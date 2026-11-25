package metrics

import (
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

type httpMetrics struct {
	requests *prometheus.CounterVec
	duration *prometheus.HistogramVec
	inFlight prometheus.Gauge
}

func newHTTP(registry *prometheus.Registry) httpMetrics {
	metrics := httpMetrics{
		requests: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "mw_http_requests_total",
			Help: "Completed API HTTP requests.",
		}, []string{"route", "method", "status_class"}),
		duration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "mw_http_request_duration_seconds",
			Help:    "API HTTP request duration in seconds.",
			Buckets: latencyBuckets(),
		}, []string{"route", "method"}),
		inFlight: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "mw_http_requests_in_flight",
			Help: "API HTTP requests currently being served.",
		}),
	}
	registry.MustRegister(metrics.requests, metrics.duration, metrics.inFlight)
	return metrics
}

func (m *Metrics) StartHTTPRequest() func() {
	m.http.inFlight.Inc()
	return m.http.inFlight.Dec
}

func (m *Metrics) RecordHTTPRequest(route, method, statusClass string, duration time.Duration) {
	m.http.requests.WithLabelValues(route, method, statusClass).Inc()
	m.http.duration.WithLabelValues(route, method).Observe(duration.Seconds())
}

func latencyBuckets() []float64 {
	return []float64{0.005, 0.025, 0.1, 0.25, 0.5, 1, 2.5, 5, 10, 25, 30, 60}
}
