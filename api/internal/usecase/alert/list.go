package alert

import (
	"context"

	"go.opentelemetry.io/otel/trace"

	"github.com/vladgrskkh/machine-whisperer/api/internal/domain/alert"
	"github.com/vladgrskkh/machine-whisperer/api/internal/domain/machine"
	"github.com/vladgrskkh/machine-whisperer/api/internal/spanutil"
)

type ListRepository interface {
	ListByOwner(ctx context.Context, ownerID, machineID string) ([]alert.Alert, error)
}

type List struct {
	tracer     trace.Tracer
	repository ListRepository
}

func NewList(repository ListRepository, tracer trace.Tracer) *List {
	return &List{tracer: tracer, repository: repository}
}

func (l *List) Execute(ctx context.Context, ownerID, machineID string) (_ []alert.Alert, err error) {
	ctx, span := l.tracer.Start(ctx, "alert.list")
	defer func() { spanutil.End(span, err, machine.ErrNotFound) }()
	return l.repository.ListByOwner(ctx, ownerID, machineID)
}
