package postgres

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/vladgrskkh/machine-whisperer/api/internal/domain/telemetry"
)

type Outbox struct {
	pool *pgxpool.Pool
}

func NewOutbox(pool *pgxpool.Pool) *Outbox {
	return &Outbox{pool: pool}
}

func (r *Outbox) Snapshot(ctx context.Context) (telemetry.OutboxSnapshot, error) {
	var snapshot telemetry.OutboxSnapshot
	err := r.pool.QueryRow(ctx, `SELECT statement_timestamp(),count(*),
		greatest(0,extract(epoch FROM statement_timestamp()-min(created_at)))::float8,
		count(*) FILTER (WHERE last_error_category IS NOT NULL),
		greatest(0,extract(epoch FROM statement_timestamp()-
			min(created_at) FILTER (WHERE last_error_category IS NOT NULL)))::float8
		FROM telemetry_outbox WHERE delivered_at IS NULL`).Scan(
		&snapshot.At, &snapshot.Pending, &snapshot.OldestPendingAgeSeconds,
		&snapshot.Retried, &snapshot.OldestRetryAgeSeconds,
	)
	if err != nil {
		return telemetry.OutboxSnapshot{}, fmt.Errorf("read outbox snapshot: %w", err)
	}
	return snapshot, nil
}

func (r *Outbox) ClaimBatch(
	ctx context.Context,
	batchSize int,
	lease time.Duration,
) (string, []telemetry.OutboxEvent, error) {
	token, err := uuid.NewRandom()
	if err != nil {
		return "", nil, fmt.Errorf("generate outbox lease token: %w", err)
	}
	// Eligibility moves to the lease expiry in the same statement, so one index also serves reclaims.
	// Updating by the locked tuple IDs keeps prepared plans from re-scanning the table.
	rows, err := r.pool.Query(ctx, `WITH due AS (
		SELECT ctid,device_id,event_id,available_at FROM telemetry_outbox
		WHERE delivered_at IS NULL AND available_at <= statement_timestamp()
		ORDER BY available_at,created_at,device_id,event_id
		LIMIT $1 FOR UPDATE SKIP LOCKED
	), claimed AS (
		UPDATE telemetry_outbox AS o
		SET lease_token=$2,lease_until=statement_timestamp()+$3::interval,
		    available_at=statement_timestamp()+$3::interval,attempt_count=attempt_count+1
		WHERE o.ctid = ANY(ARRAY(SELECT ctid FROM due))
		RETURNING o.device_id,o.event_id,o.machine_id,o.created_at,o.payload,o.attempt_count,o.traceparent
	)
	SELECT device_id::text,event_id::text,machine_id::text,created_at,payload,attempt_count,coalesce(traceparent,'')
	FROM claimed JOIN due USING (device_id,event_id)
	ORDER BY due.available_at,claimed.created_at,claimed.device_id,claimed.event_id`, batchSize, token.String(), outboxInterval(lease))
	if err != nil {
		return "", nil, fmt.Errorf("claim outbox batch: %w", err)
	}
	events, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (telemetry.OutboxEvent, error) {
		var event telemetry.OutboxEvent
		scanErr := row.Scan(&event.DeviceID, &event.EventID, &event.MachineID,
			&event.ReceivedAt, &event.Payload, &event.AttemptCount, &event.Traceparent)
		event.ReceivedAt = event.ReceivedAt.UTC()
		return event, scanErr
	})
	if err != nil {
		return "", nil, fmt.Errorf("read outbox batch: %w", err)
	}
	if len(events) == 0 {
		return "", events, nil
	}
	return token.String(), events, nil
}

// MarkDeliveredSQL is the reviewed template for acknowledging an outbox batch.
const MarkDeliveredSQL = `UPDATE telemetry_outbox AS o
		SET delivered_at=statement_timestamp(),lease_token=NULL,lease_until=NULL,last_error_category=NULL
		FROM unnest($2::text[]::uuid[],$3::text[]::uuid[]) AS keys(device_id,event_id)
		WHERE (o.device_id,o.event_id)=(keys.device_id,keys.event_id) AND o.lease_token=$1
		  AND o.delivered_at IS NULL AND o.lease_until > statement_timestamp()`

func (r *Outbox) MarkDelivered(ctx context.Context, leaseToken string, keys []telemetry.EventKey) (int64, error) {
	devices, events := outboxKeyArrays(keys)
	tag, err := r.pool.Exec(ctx, MarkDeliveredSQL, leaseToken, devices, events)
	if err != nil {
		return 0, fmt.Errorf("mark outbox rows delivered: %w", err)
	}
	return tag.RowsAffected(), nil
}

func (r *Outbox) Reschedule(
	ctx context.Context,
	leaseToken string,
	keys []telemetry.EventKey,
	backoff time.Duration,
	errorCategory string,
) (int64, error) {
	devices, events := outboxKeyArrays(keys)
	tag, err := r.pool.Exec(ctx, `UPDATE telemetry_outbox AS o
		SET available_at=statement_timestamp()+$4::interval,lease_token=NULL,lease_until=NULL,last_error_category=$5
		FROM unnest($2::text[]::uuid[],$3::text[]::uuid[]) AS keys(device_id,event_id)
		WHERE (o.device_id,o.event_id)=(keys.device_id,keys.event_id) AND o.lease_token=$1
		  AND o.delivered_at IS NULL AND o.lease_until > statement_timestamp()`,
		leaseToken, devices, events, outboxInterval(backoff), sanitizedOutboxCategory(errorCategory))
	if err != nil {
		return 0, fmt.Errorf("reschedule outbox rows: %w", err)
	}
	return tag.RowsAffected(), nil
}

func (r *Outbox) DeleteDeliveredBefore(ctx context.Context, retention time.Duration, limit int) (int64, error) {
	tag, err := r.pool.Exec(ctx, `WITH expired AS (
		SELECT device_id,event_id FROM telemetry_outbox
		WHERE delivered_at < statement_timestamp()-$1::interval
		ORDER BY delivered_at,device_id,event_id
		LIMIT $2 FOR UPDATE SKIP LOCKED
	)
	DELETE FROM telemetry_outbox AS o USING expired
	WHERE (o.device_id,o.event_id)=(expired.device_id,expired.event_id)`, outboxInterval(retention), limit)
	if err != nil {
		return 0, fmt.Errorf("delete delivered outbox rows: %w", err)
	}
	return tag.RowsAffected(), nil
}

func outboxInterval(duration time.Duration) pgtype.Interval {
	return pgtype.Interval{Microseconds: duration.Microseconds(), Valid: true}
}

func outboxKeyArrays(keys []telemetry.EventKey) ([]string, []string) {
	devices := make([]string, len(keys))
	events := make([]string, len(keys))
	for i, key := range keys {
		devices[i], events[i] = key.DeviceID, key.EventID
	}
	return devices, events
}

func sanitizedOutboxCategory(category string) string {
	switch category {
	case telemetry.OutboxAnalyticsHTTP, telemetry.OutboxAnalyticsTimeout,
		telemetry.OutboxAnalyticsRequest, telemetry.OutboxAnalyticsTransport:
		return category
	default:
		return telemetry.OutboxAnalyticsTransport
	}
}
