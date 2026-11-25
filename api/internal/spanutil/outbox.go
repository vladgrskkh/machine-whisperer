package spanutil

import (
	"context"

	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
)

const (
	OutboxDeliverSpan = "outbox.deliver"
	traceparentHeader = "traceparent"
	traceparentLength = 55
)

func SampledLocalTraceparent(ctx context.Context) string {
	span := trace.SpanFromContext(ctx)
	sc := span.SpanContext()
	if !span.IsRecording() || !sc.IsValid() || !sc.IsSampled() || sc.IsRemote() {
		return ""
	}
	carrier := propagation.MapCarrier{}
	propagation.TraceContext{}.Inject(ctx, carrier)
	return carrier.Get(traceparentHeader)
}

func SampledProducerContext(traceparent string) trace.SpanContext {
	if len(traceparent) != traceparentLength {
		return trace.SpanContext{}
	}
	ctx := propagation.TraceContext{}.Extract(
		context.Background(),
		propagation.MapCarrier{traceparentHeader: traceparent},
	)
	sc := trace.SpanContextFromContext(ctx)
	if !sc.IsSampled() {
		return trace.SpanContext{}
	}
	return sc
}
