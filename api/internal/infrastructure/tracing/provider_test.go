package tracing_test

import (
	"context"
	"net"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/trace"
	tracepb "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	"google.golang.org/grpc"

	"github.com/vladgrskkh/machine-whisperer/api/internal/infrastructure/tracing"
)

func TestProviderDisabledWithoutOTLPEndpoint(t *testing.T) {
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "")
	t.Setenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT", "")
	runtime, err := tracing.New(t.Context(), "api")
	require.NoError(t, err)
	_, span := runtime.Provider.Tracer("test").Start(t.Context(), "disabled")
	defer span.End()
	assert.False(t, span.IsRecording())
	assert.False(t, span.SpanContext().IsValid())
	assert.NoError(t, runtime.Shutdown(t.Context()))
}

func TestProviderStartsWithCollectorUnavailable(t *testing.T) {
	t.Setenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT", "http://127.0.0.1:0")
	t.Setenv("OTEL_TRACES_SAMPLER_ARG", "1")
	runtime, err := tracing.New(t.Context(), "api")
	require.NoError(t, err)
	_, span := runtime.Provider.Tracer("test").Start(t.Context(), "collector-unavailable")
	assert.True(t, span.IsRecording())
	span.End()
	assert.NoError(t, runtime.Shutdown(t.Context()))
}

func TestProviderSamplesDeliveryRootsWithSampledLinks(t *testing.T) {
	t.Setenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT", "http://127.0.0.1:0")
	t.Setenv("OTEL_TRACES_SAMPLER_ARG", "0")
	runtime, err := tracing.New(t.Context(), "api")
	require.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, runtime.Shutdown(t.Context())) })
	tracer := runtime.Provider.Tracer("test")
	sampled := trace.NewSpanContext(trace.SpanContextConfig{
		TraceID: trace.TraceID{1}, SpanID: trace.SpanID{1}, TraceFlags: trace.FlagsSampled, Remote: true,
	})

	for _, test := range []struct {
		name  string
		links []trace.Link
		want  bool
	}{
		{name: "outbox.deliver", links: []trace.Link{{SpanContext: sampled}}, want: true},
		{name: "outbox.deliver", links: []trace.Link{{SpanContext: sampled.WithTraceFlags(0)}}},
		{name: "outbox.deliver", links: []trace.Link{{SpanContext: trace.SpanContext{}}}},
		{name: "outbox.deliver"},
		{name: "ordinary.root", links: []trace.Link{{SpanContext: sampled}}},
	} {
		_, span := tracer.Start(t.Context(), test.name, trace.WithLinks(test.links...))
		assert.Equal(t, test.want, span.SpanContext().IsSampled(), test.name)
		span.End()
	}
}

func TestProviderFlushesSampledSpansAfterApplicationCancellation(t *testing.T) {
	listener, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	require.NoError(t, err)
	collector := &collector{requests: make(chan *tracepb.ExportTraceServiceRequest, 1)}
	server := grpc.NewServer()
	tracepb.RegisterTraceServiceServer(server, collector)
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(server.Stop)
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "http://"+listener.Addr().String())
	t.Setenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT", "")
	t.Setenv("OTEL_TRACES_SAMPLER_ARG", "1")
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	runtime, err := tracing.New(ctx, "api")
	require.NoError(t, err)
	tracer := runtime.Provider.Tracer("test")
	_, span := tracer.Start(ctx, "sampled")
	assert.True(t, span.SpanContext().IsSampled())
	assert.Panics(t, func() {
		defer span.End()
		panic("sensitive failure")
	})
	parent := trace.NewSpanContext(trace.SpanContextConfig{
		TraceID: trace.TraceID{1}, SpanID: trace.SpanID{1}, Remote: true,
	})
	_, span = tracer.Start(trace.ContextWithRemoteSpanContext(ctx, parent), "unsampled")
	assert.False(t, span.SpanContext().IsSampled())
	span.End()
	cancel()
	require.NoError(t, runtime.Shutdown(ctx))
	select {
	case request := <-collector.requests:
		resources := request.GetResourceSpans()
		require.Len(t, resources, 1)
		attributes := make(map[string]string)
		for _, attr := range resources[0].GetResource().GetAttributes() {
			attributes[attr.GetKey()] = attr.GetValue().GetStringValue()
		}
		assert.Equal(t, map[string]string{"service.name": "api", "service.namespace": "machine-whisperer"}, attributes)
		scopes := resources[0].GetScopeSpans()
		require.Len(t, scopes, 1)
		require.Len(t, scopes[0].GetSpans(), 1)
		assert.Equal(t, "sampled", scopes[0].GetSpans()[0].GetName())
		assert.Empty(t, scopes[0].GetSpans()[0].GetEvents())
	default:
		t.Fatal("collector received no spans at shutdown")
	}
}

type collector struct {
	tracepb.UnimplementedTraceServiceServer

	requests chan *tracepb.ExportTraceServiceRequest
}

func (c *collector) Export(
	_ context.Context,
	request *tracepb.ExportTraceServiceRequest,
) (*tracepb.ExportTraceServiceResponse, error) {
	c.requests <- request
	return &tracepb.ExportTraceServiceResponse{}, nil
}
