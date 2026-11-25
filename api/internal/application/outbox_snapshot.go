package application

import (
	"context"
	"time"
)

const (
	outboxSnapshotInterval = 30 * time.Second
	outboxSnapshotTimeout  = 2 * time.Second
)

func (a *Application) runOutboxSnapshots(ctx context.Context) {
	ticker := time.NewTicker(outboxSnapshotInterval)
	defer ticker.Stop()
	for ctx.Err() == nil {
		a.refreshOutboxSnapshot(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (a *Application) refreshOutboxSnapshot(ctx context.Context) {
	queryCtx, cancel := context.WithTimeout(ctx, outboxSnapshotTimeout)
	defer cancel()
	snapshot, err := a.outbox.Snapshot(queryCtx)
	if err != nil {
		if ctx.Err() == nil {
			a.metrics.RecordSnapshotError()
		}
		return
	}
	a.metrics.RecordSnapshot(snapshot)
}
