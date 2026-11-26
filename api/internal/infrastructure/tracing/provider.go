package tracing

import (
	"context"
	"errors"
	"net/url"
	"os"
	"strconv"
	"time"

	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.43.0"
	"go.opentelemetry.io/otel/trace"
	"go.opentelemetry.io/otel/trace/noop"

	"github.com/vladgrskkh/machine-whisperer/api/internal/spanutil"
)

const (
	httpsScheme        = "https"
	defaultSampleRatio = 0.01
	exportTimeout      = 2 * time.Second
	shutdownTimeout    = 3 * time.Second
	maxQueuedSpans     = 2048
	maxBatchSpans      = 256
)

type Runtime struct {
	Provider   trace.TracerProvider
	Propagator propagation.TraceContext
	shutdown   func(context.Context) error
}

func otlpEndpoint() string {
	if endpoint := os.Getenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT"); endpoint != "" {
		return endpoint
	}
	return os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT")
}

func New(ctx context.Context, serviceName string) (Runtime, error) {
	endpoint := otlpEndpoint()
	if endpoint == "" {
		return Runtime{Provider: noop.NewTracerProvider()}, nil
	}
	u, err := url.Parse(endpoint)
	if err != nil || (u.Scheme != "http" && u.Scheme != httpsScheme) || u.Hostname() == "" || u.User != nil ||
		u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return Runtime{}, errors.New("OTLP endpoint must be an HTTP or HTTPS origin")
	}
	ratio := defaultSampleRatio
	if value := os.Getenv("OTEL_TRACES_SAMPLER_ARG"); value != "" {
		ratio, err = strconv.ParseFloat(value, 64)
		if err != nil || !(ratio >= 0 && ratio <= 1) {
			return Runtime{}, errors.New("OTEL_TRACES_SAMPLER_ARG must be between 0 and 1")
		}
	}
	// gRPC connects lazily and the bounded batch processor never blocks producers.
	exporter, err := otlptracegrpc.New(ctx,
		otlptracegrpc.WithEndpointURL(endpoint),
		otlptracegrpc.WithTimeout(exportTimeout),
		otlptracegrpc.WithRetry(otlptracegrpc.RetryConfig{Enabled: false}),
	)
	if err != nil {
		return Runtime{}, errors.New("cannot initialize OTLP trace exporter")
	}
	provider := sdktrace.NewTracerProvider(
		sdktrace.WithResource(resource.NewSchemaless(semconv.ServiceName(serviceName),
			semconv.ServiceNamespace("machine-whisperer"))),
		sdktrace.WithoutPanicRecording(),
		sdktrace.WithSampler(sdktrace.ParentBased(outboxLinkSampler{fallback: sdktrace.TraceIDRatioBased(ratio)})),
		sdktrace.WithBatcher(exporter,
			sdktrace.WithMaxQueueSize(maxQueuedSpans),
			sdktrace.WithMaxExportBatchSize(maxBatchSpans),
			sdktrace.WithExportTimeout(exportTimeout),
		),
	)
	return Runtime{Provider: provider, shutdown: provider.Shutdown}, nil
}

type outboxLinkSampler struct{ fallback sdktrace.Sampler }

func (s outboxLinkSampler) ShouldSample(params sdktrace.SamplingParameters) sdktrace.SamplingResult {
	if params.Name == spanutil.OutboxDeliverSpan {
		for _, link := range params.Links {
			if link.SpanContext.IsValid() && link.SpanContext.IsSampled() {
				return sdktrace.SamplingResult{Decision: sdktrace.RecordAndSample}
			}
		}
	}
	return s.fallback.ShouldSample(params)
}

func (s outboxLinkSampler) Description() string {
	return "OutboxLinkSampler{" + s.fallback.Description() + "}"
}

func (r Runtime) Shutdown(ctx context.Context) error {
	if r.shutdown == nil {
		return nil
	}
	drain, cancel := context.WithTimeout(context.WithoutCancel(ctx), shutdownTimeout)
	defer cancel()
	return r.shutdown(drain)
}
