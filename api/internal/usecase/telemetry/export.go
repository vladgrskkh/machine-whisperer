package telemetry

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/vladgrskkh/machine-whisperer/api/internal/domain/telemetry"
)

type BatchSender interface {
	Send(ctx context.Context, events []telemetry.OutboxEvent) error
}

type OutboxRepository interface {
	ClaimBatch(ctx context.Context, batchSize int, lease time.Duration) (string, []telemetry.OutboxEvent, error)
	MarkDelivered(ctx context.Context, leaseToken string, keys []telemetry.EventKey) (int64, error)
	Reschedule(
		ctx context.Context,
		leaseToken string,
		keys []telemetry.EventKey,
		backoff time.Duration,
		errorCategory string,
	) error
	DeleteDeliveredBefore(ctx context.Context, retention time.Duration, limit int) (int64, error)
}

type ExportSettings struct {
	BatchSize    int
	PollInterval time.Duration
	Lease        time.Duration
	RetryBase    time.Duration
	RetryMax     time.Duration
	Retention    time.Duration
}

type ExportWorker struct {
	repository OutboxRepository
	sender     BatchSender
	settings   ExportSettings
	logger     *slog.Logger
}

func NewExportWorker(
	repository OutboxRepository,
	sender BatchSender,
	settings ExportSettings,
	logger *slog.Logger,
) *ExportWorker {
	return &ExportWorker{repository: repository, sender: sender, settings: settings, logger: logger}
}

func (w *ExportWorker) ProcessOnce(ctx context.Context) (int, error) {
	token, events, err := w.repository.ClaimBatch(ctx, w.settings.BatchSize, w.settings.Lease)
	if err != nil {
		w.logger.WarnContext(ctx, "outbox operation failed", "category", "claim")
		return 0, err
	}
	claimed := len(events)
	if claimed == 0 {
		return 0, nil
	}
	w.logger.DebugContext(ctx, "outbox batch claimed", "rows", claimed)
	if err = w.sender.Send(ctx, events); err != nil {
		return claimed, w.retry(ctx, token, events, err)
	}
	keys := make([]telemetry.EventKey, 0, claimed)
	for _, event := range events {
		keys = append(keys, telemetry.EventKey{DeviceID: event.DeviceID, EventID: event.EventID})
	}
	delivered, err := w.repository.MarkDelivered(ctx, token, keys)
	if err != nil {
		w.logger.WarnContext(ctx, "outbox operation failed", "category", "acknowledge", "rows", claimed)
		return claimed, err
	}
	w.logger.InfoContext(ctx, "outbox batch acknowledged", "rows", delivered, "claimed", claimed)
	return claimed, nil
}

func (w *ExportWorker) Run(ctx context.Context) {
	defer w.logger.InfoContext(ctx, "outbox relay stopped")
	var nextPrune time.Time
	for ctx.Err() == nil {
		if time.Now().After(nextPrune) {
			deleted, err := w.repository.DeleteDeliveredBefore(ctx, w.settings.Retention, w.settings.BatchSize)
			if err != nil {
				w.logger.WarnContext(ctx, "outbox operation failed", "category", "prune")
			} else if deleted > 0 {
				w.logger.InfoContext(ctx, "delivered outbox rows pruned", "rows", deleted)
			}
			nextPrune = time.Now().Add(w.settings.PollInterval)
		}
		claimed, err := w.ProcessOnce(ctx)
		if err == nil && claimed == w.settings.BatchSize {
			continue
		}
		timer := time.NewTimer(w.settings.PollInterval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}

func (w *ExportWorker) retry(ctx context.Context, token string, events []telemetry.OutboxEvent, sendErr error) error {
	category := analyticsErrorCategory(sendErr)
	w.logger.WarnContext(ctx, "outbox batch delivery failed", "category", category, "rows", len(events))
	if ctx.Err() != nil {
		// A canceled worker leaves the rows available for recovery after lease expiry.
		return ctx.Err()
	}
	groups := make(map[time.Duration][]telemetry.EventKey)
	for _, event := range events {
		delay := w.retryDelay(event.AttemptCount)
		groups[delay] = append(groups[delay], telemetry.EventKey{DeviceID: event.DeviceID, EventID: event.EventID})
	}
	var retryErr error
	for delay, keys := range groups {
		if err := w.repository.Reschedule(ctx, token, keys, delay, category); err != nil {
			w.logger.WarnContext(ctx, "outbox operation failed", "category", "reschedule", "rows", len(keys))
			retryErr = errors.Join(retryErr, err)
		}
	}
	return errors.Join(sendErr, retryErr)
}

func (w *ExportWorker) retryDelay(attempt int64) time.Duration {
	delay := w.settings.RetryBase
	for remaining := attempt - 1; remaining > 0 && delay < w.settings.RetryMax; remaining-- {
		if delay > w.settings.RetryMax-delay {
			return w.settings.RetryMax
		}
		delay *= 2
	}
	return delay
}

func analyticsErrorCategory(err error) string {
	switch {
	case errors.Is(err, telemetry.ErrAnalyticsHTTP):
		return telemetry.OutboxAnalyticsHTTP
	case errors.Is(err, telemetry.ErrAnalyticsTimeout):
		return telemetry.OutboxAnalyticsTimeout
	case errors.Is(err, telemetry.ErrAnalyticsRequest):
		return telemetry.OutboxAnalyticsRequest
	default:
		return telemetry.OutboxAnalyticsTransport
	}
}
