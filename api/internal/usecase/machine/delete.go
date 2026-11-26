package machine

import (
	"context"

	"go.opentelemetry.io/otel/trace"

	"github.com/vladgrskkh/machine-whisperer/api/internal/domain/machine"
	"github.com/vladgrskkh/machine-whisperer/api/internal/spanutil"
)

type DeleteRepository interface {
	DeleteByOwner(ctx context.Context, ownerUserID, machineID string) error
}

type Delete struct {
	tracer     trace.Tracer
	repository DeleteRepository
}

func NewDelete(repository DeleteRepository, tracer trace.Tracer) *Delete {
	return &Delete{tracer: tracer, repository: repository}
}

func (d *Delete) Delete(ctx context.Context, ownerUserID, machineID string) (err error) {
	ctx, span := d.tracer.Start(ctx, "machine.delete")
	defer func() { spanutil.End(span, err, machine.ErrNotFound) }()
	return d.repository.DeleteByOwner(ctx, ownerUserID, machineID)
}
