package alert

import "context"

type Acknowledge struct{ repository Repository }

func NewAcknowledge(repository Repository) *Acknowledge {
	return &Acknowledge{repository: repository}
}

func (a *Acknowledge) Execute(ctx context.Context, ownerID, alertID string) error {
	return a.repository.Acknowledge(ctx, ownerID, alertID)
}
