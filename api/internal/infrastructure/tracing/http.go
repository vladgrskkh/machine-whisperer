package tracing

import (
	"context"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	metricnoop "go.opentelemetry.io/otel/metric/noop"
	"go.opentelemetry.io/otel/propagation"
	semconv "go.opentelemetry.io/otel/semconv/v1.43.0"
	"go.opentelemetry.io/otel/trace"
	"go.opentelemetry.io/otel/trace/noop"
)

func HTTPServer(
	provider trace.TracerProvider,
	propagator propagation.TextMapPropagator,
	trustedPeer func(*http.Request) bool,
) func(http.Handler) http.Handler {
	options := append(httpOptions(provider, propagator), otelhttp.WithFilter(func(r *http.Request) bool {
		return r.URL.Path != "/healthz" && r.URL.Path != "/metrics"
	}))
	instrument := otelhttp.NewMiddleware("", options...)
	return func(next http.Handler) http.Handler {
		handler := instrument(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			next.ServeHTTP(w, r)
			span := trace.SpanFromContext(r.Context())
			if span.IsRecording() {
				r.Pattern = routePattern(r)
				if r.Pattern != "" {
					span.SetAttributes(semconv.HTTPRoute(r.Pattern))
				}
			}
		}))
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if trace.SpanContextFromContext(r.Context()).IsValid() {
				r = r.WithContext(trace.ContextWithSpanContext(r.Context(), trace.SpanContext{}))
			}
			if trustedPeer != nil && !trustedPeer(r) && r.Header.Get("Traceparent") != "" {
				r = r.WithContext(r.Context())
				r.Header = r.Header.Clone()
				r.Header.Del("Traceparent")
				r.Header.Del("Tracestate")
			}
			handler.ServeHTTP(w, r)
		})
	}
}

func routePattern(r *http.Request) string {
	route := chi.RouteContext(r.Context())
	if route == nil {
		return ""
	}
	pattern := route.RoutePattern()
	if route.RoutePath == "" {
		return pattern
	}
	// Chi retains the mount pattern when its subrouter finds no matching endpoint.
	if len(route.RoutePatterns) == 1 {
		return ""
	}
	path := r.URL.RawPath
	if path == "" {
		path = r.URL.Path
	}
	if !route.Routes.Match(chi.NewRouteContext(), r.Method, path) {
		return ""
	}
	return pattern
}

func HTTPTransport(provider trace.TracerProvider, propagator propagation.TextMapPropagator) http.RoundTripper {
	options := append(
		httpOptions(provider, propagator),
		otelhttp.WithSpanNameFormatter(func(_ string, r *http.Request) string {
			switch method := strings.ToUpper(r.Method); method {
			case http.MethodConnect, http.MethodDelete, http.MethodGet, http.MethodHead, http.MethodOptions,
				http.MethodPatch, http.MethodPost, http.MethodPut, http.MethodTrace:
				return method
			default:
				return "HTTP"
			}
		}),
	)
	return otelhttp.NewTransport(nil, options...)
}

func httpOptions(provider trace.TracerProvider, propagator propagation.TextMapPropagator) []otelhttp.Option {
	if provider == nil {
		provider = noop.NewTracerProvider()
	}
	if propagator == nil {
		propagator = propagation.TraceContext{}
	}
	return []otelhttp.Option{
		otelhttp.WithTracerProvider(httpProvider{provider}),
		otelhttp.WithPropagators(propagator),
		otelhttp.WithMeterProvider(metricnoop.NewMeterProvider()),
	}
}

// otelhttp adds request data such as URLs and user agents. Filter it before it reaches the SDK.
type httpProvider struct{ trace.TracerProvider }

func (p httpProvider) Tracer(name string, opts ...trace.TracerOption) trace.Tracer {
	return httpTracer{p.TracerProvider.Tracer(name, opts...)}
}

type httpTracer struct{ trace.Tracer }

func (t httpTracer) Start(
	ctx context.Context,
	name string,
	opts ...trace.SpanStartOption,
) (context.Context, trace.Span) {
	config := trace.NewSpanStartConfig(opts...)
	ctx, span := t.Tracer.Start(ctx, name,
		trace.WithSpanKind(config.SpanKind()),
		trace.WithTimestamp(config.Timestamp()),
		trace.WithAttributes(httpAttributes(config.Attributes())...),
	)
	wrapped := httpSpan{span}
	return trace.ContextWithSpan(ctx, wrapped), wrapped
}

type httpSpan struct{ trace.Span }

func (s httpSpan) SetAttributes(attrs ...attribute.KeyValue) {
	s.Span.SetAttributes(httpAttributes(attrs)...)
}

func (s httpSpan) SetStatus(code codes.Code, _ string) { s.Span.SetStatus(code, "") }

func (httpSpan) RecordError(error, ...trace.EventOption) {}

func (httpSpan) AddEvent(string, ...trace.EventOption) {}

func httpAttributes(attrs []attribute.KeyValue) []attribute.KeyValue {
	const minHTTPStatus, maxHTTPStatus = 100, 599
	var allowed []attribute.KeyValue
	for _, attr := range attrs {
		switch attr.Key {
		case semconv.HTTPRequestMethodKey, semconv.HTTPRouteKey:
			allowed = append(allowed, attr)
		case semconv.HTTPResponseStatusCodeKey:
			allowed = append(allowed, attr)
			// otelhttp sets server span status but supplies no error.type for a 5xx response.
			if code := attr.Value.AsInt64(); code >= http.StatusInternalServerError && code <= maxHTTPStatus {
				allowed = append(allowed, semconv.ErrorTypeKey.String(strconv.FormatInt(code, 10)))
			}
		case semconv.ErrorTypeKey:
			code, err := strconv.Atoi(attr.Value.AsString())
			if err == nil && code >= minHTTPStatus && code <= maxHTTPStatus {
				allowed = append(allowed, semconv.ErrorTypeKey.String(strconv.Itoa(code)))
			} else {
				allowed = append(allowed, semconv.ErrorTypeKey.String("network_error"))
			}
		}
	}
	return allowed
}
