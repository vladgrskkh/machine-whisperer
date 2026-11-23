package machine

import (
	"context"

	"github.com/vladgrskkh/machine-whisperer/api/internal/domain/machine"
)

type ListRepository interface {
	ListByOwner(context.Context, string) ([]machine.Machine, error)
}

type List struct{ repository ListRepository }

func NewList(repository ListRepository) *List { return &List{repository: repository} }

func (l *List) List(ctx context.Context, ownerUserID string) ([]machine.Machine, error) {
	return l.repository.ListByOwner(ctx, ownerUserID)
}
