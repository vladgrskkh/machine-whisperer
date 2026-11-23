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
	BatchSize int
	Lease     time.Duration
	RetryBase time.Duration
	RetryMax  time.Duration
	Retention time.Duration
}

type Export struct {
	repository OutboxRepository
	sender     BatchSender
	settings   ExportSettings
	logger     *slog.Logger
}

func NewExport(
	repository OutboxRepository,
	sender BatchSender,
	settings ExportSettings,
	logger *slog.Logger,
) *Export {
	return &Export{repository: repository, sender: sender, settings: settings, logger: logger}
}

func (e *Export) ProcessOnce(ctx context.Context) (int, error) {
	token, events, err := e.repository.ClaimBatch(ctx, e.settings.BatchSize, e.settings.Lease)
	if err != nil {
		e.logger.WarnContext(ctx, "outbox operation failed", "category", "claim")
		return 0, err
	}
	claimed := len(events)
	if claimed == 0 {
		return 0, nil
	}
	e.logger.DebugContext(ctx, "outbox batch claimed", "rows", claimed)
	if err = e.sender.Send(ctx, events); err != nil {
		return claimed, e.retry(ctx, token, events, err)
	}
	keys := make([]telemetry.EventKey, 0, claimed)
	for _, event := range events {
		keys = append(keys, telemetry.EventKey{DeviceID: event.DeviceID, EventID: event.EventID})
	}
	delivered, err := e.repository.MarkDelivered(ctx, token, keys)
	if err != nil {
		e.logger.WarnContext(ctx, "outbox operation failed", "category", "acknowledge", "rows", claimed)
		return claimed, err
	}
	e.logger.InfoContext(ctx, "outbox batch acknowledged", "rows", delivered, "claimed", claimed)
	return claimed, nil
}

func (e *Export) PruneDelivered(ctx context.Context) (int64, error) {
	deleted, err := e.repository.DeleteDeliveredBefore(ctx, e.settings.Retention, e.settings.BatchSize)
	if err != nil {
		e.logger.WarnContext(ctx, "outbox operation failed", "category", "prune")
	} else if deleted > 0 {
		e.logger.InfoContext(ctx, "delivered outbox rows pruned", "rows", deleted)
	}
	return deleted, err
}

func (e *Export) retry(ctx context.Context, token string, events []telemetry.OutboxEvent, sendErr error) error {
	category := analyticsErrorCategory(sendErr)
	e.logger.WarnContext(ctx, "outbox batch delivery failed", "category", category, "rows", len(events))
	if ctx.Err() != nil {
		// A canceled worker leaves the rows available for recovery after lease expiry.
		return ctx.Err()
	}
	groups := make(map[time.Duration][]telemetry.EventKey)
	for _, event := range events {
		delay := e.retryDelay(event.AttemptCount)
		groups[delay] = append(groups[delay], telemetry.EventKey{DeviceID: event.DeviceID, EventID: event.EventID})
	}
	var retryErr error
	for delay, keys := range groups {
		if err := e.repository.Reschedule(ctx, token, keys, delay, category); err != nil {
			e.logger.WarnContext(ctx, "outbox operation failed", "category", "reschedule", "rows", len(keys))
			retryErr = errors.Join(retryErr, err)
		}
	}
	return errors.Join(sendErr, retryErr)
}

func (e *Export) retryDelay(attempt int64) time.Duration {
	delay := e.settings.RetryBase
	for remaining := attempt - 1; remaining > 0 && delay < e.settings.RetryMax; remaining-- {
		if delay > e.settings.RetryMax-delay {
			return e.settings.RetryMax
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
