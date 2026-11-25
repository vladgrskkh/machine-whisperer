package alert

import (
	"context"

	"github.com/vladgrskkh/machine-whisperer/api/internal/domain/alert"
)

type ListRepository interface {
	ListByOwner(ctx context.Context, ownerID, machineID string) ([]alert.Alert, error)
}

type List struct{ repository ListRepository }

func NewList(repository ListRepository) *List { return &List{repository: repository} }

func (l *List) Execute(ctx context.Context, ownerID, machineID string) ([]alert.Alert, error) {
	return l.repository.ListByOwner(ctx, ownerID, machineID)
}
