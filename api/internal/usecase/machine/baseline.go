package machine

import (
	"context"

	"github.com/vladgrskkh/machine-whisperer/api/internal/domain/machine"
)

type BaselineRepository interface {
	GetByOwner(ctx context.Context, ownerID, machineID string) (machine.Baseline, error)
	SetByOwner(ctx context.Context, ownerID, machineID string, baseline machine.Baseline) (machine.Baseline, error)
}

type TransactionManager interface {
	WithinTransaction(context.Context, func(context.Context) error) error
}

type GetBaseline struct{ repository BaselineRepository }

func NewGetBaseline(repository BaselineRepository) *GetBaseline {
	return &GetBaseline{repository: repository}
}

func (g *GetBaseline) Execute(ctx context.Context, ownerID, machineID string) (machine.Baseline, error) {
	return g.repository.GetByOwner(ctx, ownerID, machineID)
}

type SetBaseline struct {
	repository   BaselineRepository
	transactions TransactionManager
}

func NewSetBaseline(repository BaselineRepository, transactions TransactionManager) *SetBaseline {
	return &SetBaseline{repository: repository, transactions: transactions}
}

func (s *SetBaseline) Execute(
	ctx context.Context, ownerID, machineID string, meanRMSG, stddevRMSG float64,
) (machine.Baseline, error) {
	baseline, err := machine.NewBaseline(machineID, meanRMSG, stddevRMSG)
	if err != nil {
		return machine.Baseline{}, err
	}
	var stored machine.Baseline
	err = s.transactions.WithinTransaction(ctx, func(txCtx context.Context) error {
		var storeErr error
		stored, storeErr = s.repository.SetByOwner(txCtx, ownerID, machineID, baseline)
		return storeErr
	})
	if err != nil {
		return machine.Baseline{}, err
	}
	return stored, nil
}
