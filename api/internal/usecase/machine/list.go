package machine

import (
	"context"

	"go.opentelemetry.io/otel/trace"

	"github.com/vladgrskkh/machine-whisperer/api/internal/domain/machine"
	"github.com/vladgrskkh/machine-whisperer/api/internal/spanutil"
)

type ListRepository interface {
	ListByOwner(context.Context, string) ([]machine.Machine, error)
}

type List struct {
	tracer     trace.Tracer
	repository ListRepository
}

func NewList(repository ListRepository, tracer trace.Tracer) *List {
	return &List{tracer: tracer, repository: repository}
}

//nolint:nonamedreturns // Span completion observes the returned error.
func (l *List) List(ctx context.Context, ownerUserID string) (_ []machine.Machine, err error) {
	ctx, span := l.tracer.Start(ctx, "machine.list")
	defer func() { spanutil.End(span, err) }()
	return l.repository.ListByOwner(ctx, ownerUserID)
}
