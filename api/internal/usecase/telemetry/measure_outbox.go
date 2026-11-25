package telemetry

import (
	"context"
	"time"

	"github.com/vladgrskkh/machine-whisperer/api/internal/domain/telemetry"
)

const outboxSnapshotTimeout = 2 * time.Second

type OutboxSnapshotRepository interface {
	Snapshot(context.Context) (telemetry.OutboxSnapshot, error)
}

type MeasureOutbox struct {
	repository OutboxSnapshotRepository
}

func NewMeasureOutbox(repository OutboxSnapshotRepository) *MeasureOutbox {
	return &MeasureOutbox{repository: repository}
}

func (m *MeasureOutbox) Snapshot(ctx context.Context) (telemetry.OutboxSnapshot, error) {
	ctx, cancel := context.WithTimeout(ctx, outboxSnapshotTimeout)
	defer cancel()
	return m.repository.Snapshot(ctx)
}
