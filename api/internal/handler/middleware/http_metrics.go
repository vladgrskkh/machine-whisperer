package middleware

import (
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/felixge/httpsnoop"
	"github.com/go-chi/chi/v5"
)

type HTTPRecorder interface {
	StartHTTPRequest() func()
	RecordHTTPRequest(route, method, statusClass string, duration time.Duration)
}

func HTTP(router chi.Routes, recorder HTTPRecorder) func(http.Handler) http.Handler {
	// Chi installs middleware before routes, so enumerate the completed router on first use.
	registered := sync.OnceValue(func() map[string]map[string]struct{} { return registeredRoutes(router) })
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			finish := recorder.StartHTTPRequest()
			defer finish()
			response := httpsnoop.CaptureMetrics(next, w, r)
			route, method := requestLabels(registered(), r)
			recorder.RecordHTTPRequest(route, method, statusClass(response.Code), response.Duration)
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
