package metrics

import (
	"net/http"
	"strconv"
	"strings"
	"sync"

	"github.com/felixge/httpsnoop"
	"github.com/go-chi/chi/v5"
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
			Help: "Completed business HTTP requests.",
		}, []string{"route", "method", "status_class"}),
		duration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "mw_http_request_duration_seconds",
			Help:    "Business HTTP request duration in seconds.",
			Buckets: latencyBuckets(),
		}, []string{"route", "method"}),
		inFlight: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "mw_http_requests_in_flight",
			Help: "Business HTTP requests currently being served.",
		}),
	}
	registry.MustRegister(metrics.requests, metrics.duration, metrics.inFlight)
	return metrics
}

func (m *Metrics) HTTP(router chi.Routes) func(http.Handler) http.Handler {
	// Chi installs middleware before routes, so enumerate the completed router on first use.
	registered := sync.OnceValue(func() map[string]map[string]struct{} { return registeredRoutes(router) })
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			m.http.inFlight.Inc()
			defer m.http.inFlight.Dec()
			response := httpsnoop.CaptureMetrics(next, w, r)
			route, method := requestLabels(registered(), r)
			m.http.requests.WithLabelValues(route, method, statusClass(response.Code)).Inc()
			m.http.duration.WithLabelValues(route, method).Observe(response.Duration.Seconds())
		})
	}
}

func registeredRoutes(router chi.Routes) map[string]map[string]struct{} {
	routes := make(map[string]map[string]struct{})
	_ = chi.Walk(router, func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		if routes[method] == nil {
			routes[method] = make(map[string]struct{})
		}
		if route != "/" {
			route = strings.TrimSuffix(route, "/")
		}
		routes[method][route] = struct{}{}
		return nil
	})
	return routes
}

func requestLabels(routes map[string]map[string]struct{}, r *http.Request) (string, string) {
	patterns, registered := routes[r.Method]
	if !registered {
		return "unmatched", "OTHER"
	}
	route := chi.RouteContext(r.Context()).RoutePattern()
	if _, matched := patterns[route]; !matched {
		// A mounted subrouter can leave a partial pattern after rejecting a request.
		return "unmatched", r.Method
	}
	return route, r.Method
}

func statusClass(status int) string {
	const classWidth = 100
	if status < classWidth || status >= 6*classWidth {
		return "other"
	}
	return strconv.Itoa(status/classWidth) + "xx"
}

func latencyBuckets() []float64 {
	return []float64{0.005, 0.025, 0.1, 0.25, 0.5, 1, 2.5, 5, 10, 25, 30, 60}
}
