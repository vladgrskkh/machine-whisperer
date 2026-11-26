package tracing

import (
	"context"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/riandyrn/otelchi"
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
	if provider == nil {
		provider = noop.NewTracerProvider()
	}
	if propagator == nil {
		propagator = propagation.TraceContext{}
	}
	// Use Chi's post-routing pattern. otelchi's early match uses the decoded URL.Path.
	instrument := otelchi.Middleware("",
		otelchi.WithTracerProvider(httpProvider{provider}),
		otelchi.WithPropagators(propagator),
		otelchi.WithFilter(func(r *http.Request) bool {
			return r.URL.Path != "/healthz" && r.URL.Path != "/metrics"
		}),
	)
	return func(next http.Handler) http.Handler {
		handler := instrument(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			serveHTTP(next, w, r)
		}))
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx := r.Context()
			if trace.SpanContextFromContext(ctx).IsValid() {
				ctx = trace.ContextWithSpanContext(ctx, trace.SpanContext{})
			}
			r = r.WithContext(context.WithValue(ctx, serverRequestKey{}, r))
			if trustedPeer != nil && !trustedPeer(r) && r.Header.Get("Traceparent") != "" {
				r.Header = r.Header.Clone()
				r.Header.Del("Traceparent")
				r.Header.Del("Tracestate")
			}
			handler.ServeHTTP(w, r)
		})
	}
}

type serverRequestKey struct{}

func serveHTTP(next http.Handler, w http.ResponseWriter, r *http.Request) {
	// These routers only serve HTTP. A websocket header alone makes otelchi skip status.
	if _, instrumented := trace.SpanFromContext(r.Context()).(serverSpan); instrumented &&
		strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
		req := r.WithContext(r.Context())
		r.Header = r.Header.Clone()
		r.Header.Del("Upgrade")
		r = req
	}
	next.ServeHTTP(w, r)
}

func HTTPTransport(provider trace.TracerProvider, propagator propagation.TextMapPropagator) http.RoundTripper {
	options := append(
		httpOptions(provider, propagator),
		otelhttp.WithSpanNameFormatter(func(_ string, r *http.Request) string {
			return spanMethod(requestMethod(r.Method))
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

// Filter request data from HTTP instrumentation before it reaches the SDK.
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
	kind := config.SpanKind()
	request, _ := ctx.Value(serverRequestKey{}).(*http.Request)
	server := kind == trace.SpanKindServer && request != nil
	if server {
		name = spanMethod(requestMethod(request.Method))
	}
	ctx, span := t.Tracer.Start(ctx, name,
		trace.WithSpanKind(kind),
		trace.WithTimestamp(config.Timestamp()),
		trace.WithAttributes(httpAttributes(config.Attributes())...),
	)
	wrapped := trace.Span(httpSpan{span})
	if server {
		wrapped = serverSpan{httpSpan: httpSpan{span}, request: request, method: name}
	}
	return trace.ContextWithSpan(ctx, wrapped), wrapped
}

type serverSpan struct {
	httpSpan

	request *http.Request
	method  string
}

func (s serverSpan) SetName(_ string) {
	route := chi.RouteContext(s.request.Context())
	if route == nil || len(route.RoutePatterns) == 0 {
		return
	}
	if route.RoutePath != "" {
		// Chi can retain a mount pattern even when the subrouter rejects the request.
		if len(route.RoutePatterns) == 1 {
			return
		}
		path := s.request.URL.RawPath
		if path == "" {
			path = s.request.URL.Path
		}
		if !route.Routes.Match(chi.NewRouteContext(), s.request.Method, path) {
			return
		}
	}
	pattern := route.RoutePattern()
	s.Span.SetName(s.method + " " + pattern)
	s.Span.SetAttributes(semconv.HTTPRoute(pattern))
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
		case "http.method": // otelchi uses the legacy HTTP attributes.
			allowed = append(allowed, semconv.HTTPRequestMethodKey.String(requestMethod(attr.Value.AsString())))
		case semconv.HTTPRequestMethodKey:
			allowed = append(allowed, attr)
		case "http.status_code", semconv.HTTPResponseStatusCodeKey:
			allowed = append(allowed, semconv.HTTPResponseStatusCodeKey.Int64(attr.Value.AsInt64()))
			// HTTP instrumentation can set span status without an error.type attribute.
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

func requestMethod(raw string) string {
	switch method := strings.ToUpper(raw); method {
	case http.MethodConnect, http.MethodDelete, http.MethodGet, http.MethodHead, http.MethodOptions,
		http.MethodPatch, http.MethodPost, http.MethodPut, http.MethodTrace:
		return method
	default:
		return semconv.HTTPRequestMethodOther.Value.AsString()
	}
}

func spanMethod(method string) string {
	if method == semconv.HTTPRequestMethodOther.Value.AsString() {
		return "HTTP"
	}
	return method
}
