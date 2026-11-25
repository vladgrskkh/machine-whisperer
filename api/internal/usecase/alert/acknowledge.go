package alert

import "context"

type AcknowledgeRepository interface {
	Acknowledge(ctx context.Context, ownerID, alertID string) (bool, error)
}

type AcknowledgeRecorder interface {
	RecordAcknowledgement()
}

type Acknowledge struct {
	repository AcknowledgeRepository
	recorder   AcknowledgeRecorder
}

func NewAcknowledge(repository AcknowledgeRepository, recorder AcknowledgeRecorder) *Acknowledge {
	return &Acknowledge{repository: repository, recorder: recorder}
}

func (a *Acknowledge) Execute(ctx context.Context, ownerID, alertID string) error {
	changed, err := a.repository.Acknowledge(ctx, ownerID, alertID)
	if err == nil && changed {
		a.recorder.RecordAcknowledgement()
	}
	return err
}
