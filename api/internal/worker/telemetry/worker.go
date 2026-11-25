package telemetry

import (
	"context"
	"log/slog"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	"github.com/vladgrskkh/machine-whisperer/api/internal/domain/telemetry"
	"github.com/vladgrskkh/machine-whisperer/api/internal/spanutil"
)

const maxOutboxLinks = 32

type exportUseCase interface {
	ClaimBatch(ctx context.Context) (string, []telemetry.OutboxEvent, error)
	DeliverBatch(ctx context.Context, token string, events []telemetry.OutboxEvent) error
	PruneDelivered(ctx context.Context) (int64, error)
}

type Worker struct {
	tracer       trace.Tracer
	export       exportUseCase
	batchSize    int
	pollInterval time.Duration
	logger       *slog.Logger
}

func New(
	export exportUseCase,
	batchSize int,
	pollInterval time.Duration,
	logger *slog.Logger,
	tracer trace.Tracer,
) *Worker {
	return &Worker{tracer: tracer, export: export, batchSize: batchSize, pollInterval: pollInterval, logger: logger}
}

func (w *Worker) Run(ctx context.Context) {
	// Polling and pruning are background operations even when Run is called from a traced context.
	ctx = trace.ContextWithSpanContext(ctx, trace.SpanContext{})
	w.logger.InfoContext(ctx, "outbox relay started")
	defer w.logger.InfoContext(ctx, "outbox relay stopped")

	var nextPrune time.Time
	for ctx.Err() == nil {
		if time.Now().After(nextPrune) {
			// The use case logs pruning failures and the next poll retries them.
			_, _ = w.export.PruneDelivered(ctx)
			nextPrune = time.Now().Add(w.pollInterval)
		}
		claimed, err := w.processOnce(ctx)
		if err == nil && claimed == w.batchSize {
			continue
		}
		timer := time.NewTimer(w.pollInterval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}

func (w *Worker) processOnce(ctx context.Context) (int, error) {
	token, events, err := w.export.ClaimBatch(ctx)
	if err != nil || len(events) == 0 {
		return 0, err
	}
	links, dropped := outboxLinks(events)
	ctx, span := w.tracer.Start(ctx, spanutil.OutboxDeliverSpan, trace.WithNewRoot(), trace.WithLinks(links...))
	defer func() { spanutil.End(span, err) }()
	if dropped > 0 {
		span.SetAttributes(attribute.Int("outbox.links.dropped", dropped))
	}
	err = w.export.DeliverBatch(ctx, token, events)
	return len(events), err
}

type producerSpanID struct {
	trace trace.TraceID
	span  trace.SpanID
}

func outboxLinks(events []telemetry.OutboxEvent) ([]trace.Link, int) {
	var links []trace.Link
	seen := make(map[producerSpanID]struct{})
	for _, event := range events {
		sc := spanutil.SampledProducerContext(event.Traceparent)
		if !sc.IsValid() {
			continue
		}
		id := producerSpanID{trace: sc.TraceID(), span: sc.SpanID()}
		if _, exists := seen[id]; exists {
			continue
		}
		seen[id] = struct{}{}
		if len(links) < maxOutboxLinks {
			links = append(links, trace.Link{SpanContext: sc})
		}
	}
	return links, len(seen) - len(links)
}
