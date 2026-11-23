package telemetry

import (
	"context"

	"github.com/vladgrskkh/machine-whisperer/analytics/internal/domain"
)

type TelemetryRepository interface {
	StoreBatch(ctx context.Context, events []domain.Event) error
}

type IngestBatch struct {
	repository TelemetryRepository
}

func NewIngestBatch(repository TelemetryRepository) *IngestBatch {
	return &IngestBatch{repository: repository}
}

func (i *IngestBatch) Execute(ctx context.Context, events []domain.Event) error {
	return i.repository.StoreBatch(ctx, events)
}
