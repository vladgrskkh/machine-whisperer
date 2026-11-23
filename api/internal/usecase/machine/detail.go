package machine

import (
	"context"

	"github.com/vladgrskkh/machine-whisperer/api/internal/domain/machine"
)

type DetailRepository interface {
	FindByOwner(ctx context.Context, ownerUserID, machineID string) (machine.Machine, error)
}

type Detail struct{ repository DetailRepository }

func NewDetail(repository DetailRepository) *Detail { return &Detail{repository: repository} }

func (d *Detail) Detail(ctx context.Context, ownerUserID, machineID string) (machine.Machine, error) {
	return d.repository.FindByOwner(ctx, ownerUserID, machineID)
}
