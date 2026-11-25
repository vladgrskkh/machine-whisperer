package telemetry

import (
	"context"
	"encoding/base64"
	"encoding/json"

	"go.opentelemetry.io/otel/trace"

	"github.com/vladgrskkh/machine-whisperer/analytics/internal/domain"
	"github.com/vladgrskkh/machine-whisperer/analytics/internal/spanutil"
)

type TelemetryHistoryRepository interface {
	List(ctx context.Context, query domain.HistoryQuery) ([]domain.Event, error)
}

type ListHistory struct {
	tracer     trace.Tracer
	repository TelemetryHistoryRepository
}

func NewListHistory(repository TelemetryHistoryRepository, tracer trace.Tracer) *ListHistory {
	return &ListHistory{tracer: tracer, repository: repository}
}

//nolint:nonamedreturns // Span completion observes the returned error.
func (h *ListHistory) Execute(ctx context.Context, query domain.HistoryQuery) (_ domain.HistoryPage, err error) {
	ctx, span := h.tracer.Start(ctx, "telemetry.history.list")
	defer func() { spanutil.End(span, err) }()
	limit := query.Limit
	query.Limit++
	events, err := h.repository.List(ctx, query)
	if err != nil {
		return domain.HistoryPage{}, err
	}
	if len(events) <= limit {
		return domain.HistoryPage{Events: events}, nil
	}
	last := events[limit-1]
	data, err := json.Marshal(domain.HistoryCursor{
		MachineID:  query.MachineID,
		ObservedAt: last.Payload.ObservedAt,
		DeviceID:   last.DeviceID,
		EventID:    last.EventID,
	})
	if err != nil {
		return domain.HistoryPage{}, err
	}
	next := base64.RawURLEncoding.EncodeToString(data)
	return domain.HistoryPage{Events: events[:limit], NextCursor: &next}, nil
}
