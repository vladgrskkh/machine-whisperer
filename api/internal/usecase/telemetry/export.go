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
	) (int64, error)
	DeleteDeliveredBefore(ctx context.Context, retention time.Duration, limit int) (int64, error)
}

type ExportMetrics interface {
	RecordClaimed(rows int)
	RecordDelivered(rows int64)
	RecordRetried(reason string, rows int64)
	RecordOutboxError(operation string)
}

const (
	OutboxOperationClaim       = "claim"
	OutboxOperationAcknowledge = "acknowledge"
	OutboxOperationReschedule  = "reschedule"
	OutboxOperationPrune       = "prune"
)

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
	metrics    ExportMetrics
}

func NewExport(
	repository OutboxRepository,
	sender BatchSender,
	settings ExportSettings,
	logger *slog.Logger,
	metrics ExportMetrics,
) *Export {
	return &Export{repository: repository, sender: sender, settings: settings, logger: logger, metrics: metrics}
}

func (e *Export) ProcessOnce(ctx context.Context) (int, error) {
	token, events, err := e.repository.ClaimBatch(ctx, e.settings.BatchSize, e.settings.Lease)
	if err != nil {
		e.operationFailed(ctx, OutboxOperationClaim, 0)
		return 0, err
	}
	claimed := len(events)
	if claimed == 0 {
		return 0, nil
	}
	e.metrics.RecordClaimed(claimed)
	e.logger.DebugContext(ctx, "outbox batch claimed", "rows", claimed)
	err = e.sender.Send(ctx, events)
	if err != nil {
		return claimed, e.retry(ctx, token, events, err)
	}
	keys := make([]telemetry.EventKey, 0, claimed)
	for _, event := range events {
		keys = append(keys, telemetry.EventKey{DeviceID: event.DeviceID, EventID: event.EventID})
	}
	delivered, err := e.repository.MarkDelivered(ctx, token, keys)
	if err != nil {
		e.operationFailed(ctx, OutboxOperationAcknowledge, claimed)
		return claimed, err
	}
	e.metrics.RecordDelivered(delivered)
	e.logger.InfoContext(ctx, "outbox batch acknowledged", "rows", delivered, "claimed", claimed)
	return claimed, nil
}

func (e *Export) PruneDelivered(ctx context.Context) (int64, error) {
	deleted, err := e.repository.DeleteDeliveredBefore(ctx, e.settings.Retention, e.settings.BatchSize)
	if err != nil {
		e.operationFailed(ctx, OutboxOperationPrune, 0)
	} else if deleted > 0 {
		e.logger.InfoContext(ctx, "delivered outbox rows pruned", "rows", deleted)
	}
	return deleted, err
}

func (e *Export) retry(ctx context.Context, token string, events []telemetry.OutboxEvent, sendErr error) error {
	category := telemetry.AnalyticsFailureCategory(sendErr)
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
		retried, err := e.repository.Reschedule(ctx, token, keys, delay, category)
		if err != nil {
			e.operationFailed(ctx, OutboxOperationReschedule, len(keys))
			retryErr = errors.Join(retryErr, err)
			continue
		}
		e.metrics.RecordRetried(category, retried)
	}
	return errors.Join(sendErr, retryErr)
}

func (e *Export) operationFailed(ctx context.Context, operation string, rows int) {
	e.metrics.RecordOutboxError(operation)
	if rows == 0 {
		e.logger.WarnContext(ctx, "outbox operation failed", "category", operation)
		return
	}
	e.logger.WarnContext(ctx, "outbox operation failed", "category", operation, "rows", rows)
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
