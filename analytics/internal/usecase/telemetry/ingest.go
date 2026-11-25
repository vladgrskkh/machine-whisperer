package telemetry

import (
	"context"

	"github.com/vladgrskkh/machine-whisperer/analytics/internal/domain"
)

type TelemetryRepository interface {
	StoreBatch(ctx context.Context, events []domain.Event) error
}

type BatchRecorder interface {
	RecordStoredBatch(events int)
	RecordBatchStorageError()
}

type IngestBatch struct {
	repository TelemetryRepository
	recorder   BatchRecorder
}

func NewIngestBatch(repository TelemetryRepository, recorder BatchRecorder) *IngestBatch {
	return &IngestBatch{repository: repository, recorder: recorder}
}

func (i *IngestBatch) Execute(ctx context.Context, events []domain.Event) error {
	if err := i.repository.StoreBatch(ctx, events); err != nil {
		i.recorder.RecordBatchStorageError()
		return err
	}
	i.recorder.RecordStoredBatch(len(events))
	return nil
}
