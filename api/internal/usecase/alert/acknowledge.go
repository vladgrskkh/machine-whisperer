package alert

import (
	"context"

	"go.opentelemetry.io/otel/trace"

	"github.com/vladgrskkh/machine-whisperer/api/internal/domain/alert"
	"github.com/vladgrskkh/machine-whisperer/api/internal/spanutil"
)

type AcknowledgeRepository interface {
	Acknowledge(ctx context.Context, ownerID, alertID string) (bool, error)
}

type AcknowledgeRecorder interface {
	RecordAcknowledgement()
}

type Acknowledge struct {
	tracer     trace.Tracer
	repository AcknowledgeRepository
	recorder   AcknowledgeRecorder
}

func NewAcknowledge(repository AcknowledgeRepository, recorder AcknowledgeRecorder, tracer trace.Tracer) *Acknowledge {
	return &Acknowledge{repository: repository, recorder: recorder, tracer: tracer}
}

//nolint:nonamedreturns // Span completion observes the returned error.
func (a *Acknowledge) Execute(ctx context.Context, ownerID, alertID string) (err error) {
	ctx, span := a.tracer.Start(ctx, "alert.acknowledge")
	defer func() { spanutil.End(span, err, alert.ErrNotFound) }()
	changed, err := a.repository.Acknowledge(ctx, ownerID, alertID)
	if err == nil && changed {
		a.recorder.RecordAcknowledgement()
	}
	return err
}
