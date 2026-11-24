package telemetry

import (
	"context"

	"github.com/vladgrskkh/machine-whisperer/api/internal/domain/machine"
	"github.com/vladgrskkh/machine-whisperer/api/internal/domain/telemetry"
)

type HistoryClient interface {
	List(ctx context.Context, machineID string, limit int, cursor string) (telemetry.HistoryPage, error)
}

type HistoryMachineRepository interface {
	FindByOwner(ctx context.Context, ownerID, machineID string) (machine.Machine, error)
}

type ListHistory struct {
	machines HistoryMachineRepository
	client   HistoryClient
}

func NewListHistory(machines HistoryMachineRepository, client HistoryClient) *ListHistory {
	return &ListHistory{machines: machines, client: client}
}

func (l *ListHistory) Execute(
	ctx context.Context,
	ownerID, machineID string,
	limit int,
	cursor string,
) (telemetry.HistoryPage, error) {
	if _, err := l.machines.FindByOwner(ctx, ownerID, machineID); err != nil {
		return telemetry.HistoryPage{}, err
	}
	return l.client.List(ctx, machineID, limit, cursor)
}
