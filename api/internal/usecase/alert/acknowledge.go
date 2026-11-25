package alert

import "context"

type AcknowledgeRepository interface {
	Acknowledge(ctx context.Context, ownerID, alertID string) (bool, error)
}

type Acknowledge struct{ repository AcknowledgeRepository }

func NewAcknowledge(repository AcknowledgeRepository) *Acknowledge {
	return &Acknowledge{repository: repository}
}

func (a *Acknowledge) Execute(ctx context.Context, ownerID, alertID string) (bool, error) {
	return a.repository.Acknowledge(ctx, ownerID, alertID)
}
