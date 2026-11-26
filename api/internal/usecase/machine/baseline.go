package machine

import (
	"context"

	"go.opentelemetry.io/otel/trace"

	"github.com/vladgrskkh/machine-whisperer/api/internal/domain/machine"
	"github.com/vladgrskkh/machine-whisperer/api/internal/spanutil"
)

type BaselineRepository interface {
	GetByOwner(ctx context.Context, ownerID, machineID string) (machine.Baseline, error)
	SetByOwner(ctx context.Context, ownerID, machineID string, baseline machine.Baseline) (machine.Baseline, error)
}

type TransactionManager interface {
	WithinTransaction(context.Context, func(context.Context) error) error
}

type GetBaseline struct {
	tracer     trace.Tracer
	repository BaselineRepository
}

func NewGetBaseline(repository BaselineRepository, tracer trace.Tracer) *GetBaseline {
	return &GetBaseline{tracer: tracer, repository: repository}
}

func (g *GetBaseline) Execute(ctx context.Context, ownerID, machineID string) (_ machine.Baseline, err error) {
	ctx, span := g.tracer.Start(ctx, "machine.baseline.get")
	defer func() { spanutil.End(span, err, machine.ErrNotFound, machine.ErrBaselineNotFound) }()
	return g.repository.GetByOwner(ctx, ownerID, machineID)
}

type SetBaseline struct {
	tracer       trace.Tracer
	repository   BaselineRepository
	transactions TransactionManager
}

func NewSetBaseline(repository BaselineRepository, transactions TransactionManager, tracer trace.Tracer) *SetBaseline {
	return &SetBaseline{tracer: tracer, repository: repository, transactions: transactions}
}

func (s *SetBaseline) Execute(
	ctx context.Context, ownerID, machineID string, meanRMSG, stddevRMSG float64,
) (_ machine.Baseline, err error) {
	ctx, span := s.tracer.Start(ctx, "machine.baseline.set")
	defer func() { spanutil.End(span, err, machine.ErrInvalidBaseline, machine.ErrNotFound) }()
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
