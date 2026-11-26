package machine

import (
	"context"

	"go.opentelemetry.io/otel/trace"

	"github.com/vladgrskkh/machine-whisperer/api/internal/domain/machine"
	"github.com/vladgrskkh/machine-whisperer/api/internal/spanutil"
)

type DetailRepository interface {
	FindByOwner(ctx context.Context, ownerUserID, machineID string) (machine.Machine, error)
}

type Detail struct {
	tracer     trace.Tracer
	repository DetailRepository
}

func NewDetail(repository DetailRepository, tracer trace.Tracer) *Detail {
	return &Detail{tracer: tracer, repository: repository}
}

func (d *Detail) Detail(ctx context.Context, ownerUserID, machineID string) (_ machine.Machine, err error) {
	ctx, span := d.tracer.Start(ctx, "machine.detail")
	defer func() { spanutil.End(span, err, machine.ErrNotFound) }()
	return d.repository.FindByOwner(ctx, ownerUserID, machineID)
}
