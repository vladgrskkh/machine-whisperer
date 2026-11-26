package telemetry

import (
	"context"
	"log/slog"

	"go.opentelemetry.io/otel/trace"

	"github.com/vladgrskkh/machine-whisperer/analytics/internal/domain"
	"github.com/vladgrskkh/machine-whisperer/analytics/internal/spanutil"
)

type TelemetryRepository interface {
	StoreBatch(ctx context.Context, events []domain.Event) error
}

type BatchRecorder interface {
	RecordStoredBatch(events int)
	RecordBatchStorageError()
}

type IngestBatch struct {
	tracer     trace.Tracer
	repository TelemetryRepository
	recorder   BatchRecorder
	logger     *slog.Logger
}

func NewIngestBatch(
	repository TelemetryRepository, recorder BatchRecorder, logger *slog.Logger, tracer trace.Tracer,
) *IngestBatch {
	return &IngestBatch{repository: repository, recorder: recorder, logger: logger, tracer: tracer}
}

func (i *IngestBatch) Execute(ctx context.Context, events []domain.Event) (err error) {
	ctx, span := i.tracer.Start(ctx, "telemetry.batch.ingest")
	defer func() { spanutil.End(span, err) }()
	if err = i.repository.StoreBatch(ctx, events); err != nil {
		i.recorder.RecordBatchStorageError()
		i.logger.ErrorContext(ctx, "analytics telemetry batch store failed", "rows", len(events))
		return err
	}
	i.recorder.RecordStoredBatch(len(events))
	return nil
}
