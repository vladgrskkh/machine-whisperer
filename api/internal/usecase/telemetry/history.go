package telemetry

import (
	"context"
	"errors"

	"go.opentelemetry.io/otel/trace"

	"github.com/vladgrskkh/machine-whisperer/api/internal/domain/machine"
	"github.com/vladgrskkh/machine-whisperer/api/internal/domain/telemetry"
	"github.com/vladgrskkh/machine-whisperer/api/internal/spanutil"
)

type HistoryClient interface {
	List(ctx context.Context, machineID string, limit int, cursor string) (telemetry.HistoryPage, error)
}

type HistoryMachineRepository interface {
	FindByOwner(ctx context.Context, ownerID, machineID string) (machine.Machine, error)
}

type ListHistory struct {
	tracer   trace.Tracer
	machines HistoryMachineRepository
	client   HistoryClient
}

func NewListHistory(machines HistoryMachineRepository, client HistoryClient, tracer trace.Tracer) *ListHistory {
	return &ListHistory{tracer: tracer, machines: machines, client: client}
}

//nolint:nonamedreturns // Span completion observes the returned error.
func (l *ListHistory) Execute(
	ctx context.Context,
	ownerID, machineID string,
	limit int,
	cursor string,
) (_ telemetry.HistoryPage, err error) {
	ctx, span := l.tracer.Start(ctx, "telemetry.history.list")
	defer func() { spanutil.End(span, err, machine.ErrNotFound, telemetry.ErrInvalidHistoryQuery) }()
	if _, err := l.machines.FindByOwner(ctx, ownerID, machineID); err != nil {
		if errors.Is(err, machine.ErrNotFound) {
			return telemetry.HistoryPage{}, err
		}
		return telemetry.HistoryPage{}, telemetry.ErrHistoryUnavailable
	}
	return l.client.List(ctx, machineID, limit, cursor)
}
