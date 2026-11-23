package telemetry_test

import (
	"context"
	"log/slog"
	"testing"
	"testing/synctest"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/vladgrskkh/machine-whisperer/api/internal/worker/telemetry"
)

func TestWorkerProcessesAtStartupAndStopsOnCancellation(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		export := &idleExport{}
		worker := telemetry.New(export, 1000, time.Hour, slog.New(slog.DiscardHandler))
		stopped := make(chan struct{})
		go func() {
			defer close(stopped)
			worker.Run(ctx)
		}()

		synctest.Wait()
		assert.Equal(t, 1, export.prunes)
		assert.Equal(t, 1, export.batches)

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

func (e *idleExport) ProcessOnce(context.Context) (int, error) {
	e.batches++
	return 0, nil
}

func (e *idleExport) PruneDelivered(context.Context) (int64, error) {
	e.prunes++
	return 0, nil
}
