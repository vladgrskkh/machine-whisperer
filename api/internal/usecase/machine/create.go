package machine

import (
	"context"
	"fmt"
	"time"

	"github.com/vladgrskkh/machine-whisperer/api/internal/domain/machine"
)

type CreateRepository interface {
	Create(context.Context, machine.Machine) error
}

type IDGenerator interface {
	Generate(context.Context) (string, error)
}

type Create struct {
	repository CreateRepository
	ids        IDGenerator
}

func NewCreate(repository CreateRepository, ids IDGenerator) *Create {
	return &Create{repository: repository, ids: ids}
}

func (c *Create) Create(ctx context.Context, ownerUserID, name string) (machine.Machine, error) {
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
