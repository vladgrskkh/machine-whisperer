package alert

import (
	"context"

	"github.com/vladgrskkh/machine-whisperer/api/internal/domain/alert"
)

type Repository interface {
	ListByOwner(ctx context.Context, ownerID, machineID string) ([]alert.Alert, error)
	Acknowledge(ctx context.Context, ownerID, alertID string) error
}

type List struct{ repository Repository }

func NewList(repository Repository) *List { return &List{repository: repository} }

func (l *List) Execute(ctx context.Context, ownerID, machineID string) ([]alert.Alert, error) {
	return l.repository.ListByOwner(ctx, ownerID, machineID)
}
