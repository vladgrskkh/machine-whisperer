package telemetry_test

import (
	"context"
	"log/slog"
	"testing"
	"testing/synctest"
	"time"

	"github.com/stretchr/testify/assert"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	domain "github.com/vladgrskkh/machine-whisperer/api/internal/domain/telemetry"
	"github.com/vladgrskkh/machine-whisperer/api/internal/worker/telemetry"
)

func TestWorkerProcessesAtStartupAndStopsOnCancellation(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		export := &idleExport{}
		recorder := tracetest.NewSpanRecorder()
		provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
		t.Cleanup(func() { assert.NoError(t, provider.Shutdown(context.WithoutCancel(t.Context()))) })
		worker := telemetry.New(export, 1000, time.Hour, slog.New(slog.DiscardHandler), provider.Tracer("test"))
		stopped := make(chan struct{})
		go func() {
			defer close(stopped)
			worker.Run(ctx)
		}()

		synctest.Wait()
		assert.Equal(t, 1, export.prunes)
		assert.Equal(t, 1, export.batches)
		assert.Empty(t, recorder.Started())

		cancel()
		synctest.Wait()
		select {
		case <-stopped:
		default:
			t.Fatal("worker did not stop after cancellation")
		}
		assert.Equal(t, 1, export.batches)
	})
}

type idleExport struct {
	batches int
	prunes  int
}

func (e *idleExport) ClaimBatch(context.Context) (string, []domain.OutboxEvent, error) {
	e.batches++
	return "", nil, nil
}

func (*idleExport) DeliverBatch(context.Context, string, []domain.OutboxEvent) error {
	panic("worker delivered an empty batch")
}

func (e *idleExport) PruneDelivered(context.Context) (int64, error) {
	e.prunes++
	return 0, nil
}
