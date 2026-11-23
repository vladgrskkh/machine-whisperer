package telemetry

import (
	"context"
	"log/slog"
	"time"
)

type exportUseCase interface {
	ProcessOnce(ctx context.Context) (int, error)
	PruneDelivered(ctx context.Context) (int64, error)
}

type Worker struct {
	export       exportUseCase
	batchSize    int
	pollInterval time.Duration
	logger       *slog.Logger
}

func New(export exportUseCase, batchSize int, pollInterval time.Duration, logger *slog.Logger) *Worker {
	return &Worker{export: export, batchSize: batchSize, pollInterval: pollInterval, logger: logger}
}

func (w *Worker) Run(ctx context.Context) {
	w.logger.InfoContext(ctx, "outbox relay started")
	defer w.logger.InfoContext(ctx, "outbox relay stopped")

	var nextPrune time.Time
	for ctx.Err() == nil {
		if time.Now().After(nextPrune) {
			// The use case logs pruning failures and the next poll retries them.
			_, _ = w.export.PruneDelivered(ctx)
			nextPrune = time.Now().Add(w.pollInterval)
		}
		claimed, err := w.export.ProcessOnce(ctx)
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
