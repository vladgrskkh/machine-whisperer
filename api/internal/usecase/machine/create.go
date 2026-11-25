package machine

import (
	"context"
	"fmt"
	"time"

	"go.opentelemetry.io/otel/trace"

	"github.com/vladgrskkh/machine-whisperer/api/internal/domain/machine"
	"github.com/vladgrskkh/machine-whisperer/api/internal/spanutil"
)

type CreateRepository interface {
	Create(context.Context, machine.Machine) error
}

type IDGenerator interface {
	Generate(context.Context) (string, error)
}

type Create struct {
	tracer     trace.Tracer
	repository CreateRepository
	ids        IDGenerator
}

func NewCreate(repository CreateRepository, ids IDGenerator, tracer trace.Tracer) *Create {
	return &Create{tracer: tracer, repository: repository, ids: ids}
}

//nolint:nonamedreturns // Span completion observes the returned error.
func (c *Create) Create(ctx context.Context, ownerUserID, name string) (_ machine.Machine, err error) {
	ctx, span := c.tracer.Start(ctx, "machine.create")
	defer func() { spanutil.End(span, err, machine.ErrInvalidName, machine.ErrNotFound) }()
	validName, err := machine.ValidateName(name)
	if err != nil {
		return machine.Machine{}, err
	}
	id, err := c.ids.Generate(ctx)
	if err != nil {
		return machine.Machine{}, fmt.Errorf("generate machine ID: %w", err)
	}
	value := machine.Machine{
		ID: id, OwnerUserID: ownerUserID, Name: validName, CreatedAt: time.Now().UTC().Truncate(time.Microsecond),
	}
	if err = c.repository.Create(ctx, value); err != nil {
		return machine.Machine{}, err
	}
	return value, nil
}
