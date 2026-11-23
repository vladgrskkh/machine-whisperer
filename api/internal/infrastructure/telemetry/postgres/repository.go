package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgerrcode"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/vladgrskkh/machine-whisperer/api/internal/domain/machine"
	"github.com/vladgrskkh/machine-whisperer/api/internal/domain/telemetry"
	transaction "github.com/vladgrskkh/machine-whisperer/api/internal/infrastructure/transaction/postgres"
)

var errTransactionRequired = errors.New("telemetry operation requires a transaction")

type Repository struct{}

func New() *Repository { return &Repository{} }

func (r *Repository) LockMachineForTelemetry(ctx context.Context, machineID string) (*time.Time, time.Time, error) {
	tx := transaction.FromContext(ctx)
	if tx == nil {
		return nil, time.Time{}, errTransactionRequired
	}

	// Evaluate the clock after the row lock, including any time spent waiting for it.
	var lastObservedAt *time.Time
	var dbNow time.Time
	err := tx.QueryRow(ctx, `WITH locked_machine AS MATERIALIZED (
		SELECT last_telemetry_observed_at FROM machines WHERE id=$1 FOR UPDATE
	)
	SELECT last_telemetry_observed_at,clock_timestamp() FROM locked_machine`, machineID).Scan(&lastObservedAt, &dbNow)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, time.Time{}, machine.ErrNotFound
	}
	if err != nil {
		return nil, time.Time{}, fmt.Errorf("lock machine for telemetry: %w", err)
	}
	if lastObservedAt != nil {
		*lastObservedAt = lastObservedAt.UTC()
	}
	return lastObservedAt, dbNow.UTC(), nil
}

func (r *Repository) FindTelemetryEvent(ctx context.Context, deviceID, eventID string) ([32]byte, bool, error) {
	tx := transaction.FromContext(ctx)
	if tx == nil {
		return [32]byte{}, false, errTransactionRequired
	}
	var hash []byte
	err := tx.QueryRow(ctx, `SELECT payload_hash FROM telemetry_events WHERE device_id=$1 AND event_id=$2`, deviceID, eventID).
		Scan(&hash)
	if errors.Is(err, pgx.ErrNoRows) {
		return [32]byte{}, false, nil
	}
	if err != nil {
		return [32]byte{}, false, fmt.Errorf("find telemetry event: %w", err)
	}
	return [32]byte(hash), true, nil
}

func (r *Repository) InsertTelemetryEvent(ctx context.Context, event telemetry.Event) error {
	tx := transaction.FromContext(ctx)
	if tx == nil {
		return errTransactionRequired
	}
	observedAt := event.ObservedAt.UTC().Truncate(time.Microsecond)
	_, err := tx.Exec(
		ctx,
		`INSERT INTO telemetry_events (device_id,event_id,machine_id,observed_at,payload_hash,expires_at)
		VALUES ($1,$2,$3,$4,$5,$6)`,
		event.DeviceID,
		event.EventID,
		event.MachineID,
		observedAt,
		event.PayloadHash[:],
		observedAt.Add(telemetry.RetryWindow),
	)
	if err != nil {
		return telemetryInsertError("insert telemetry event", err)
	}
	return nil
}

func (r *Repository) DeleteExpiredTelemetryEvents(ctx context.Context, dbNow time.Time) error {
	tx := transaction.FromContext(ctx)
	if tx == nil {
		return errTransactionRequired
	}
	_, err := tx.Exec(ctx, `DELETE FROM telemetry_events WHERE expires_at <= $1`, dbNow)
	if err != nil {
		return fmt.Errorf("delete expired telemetry events: %w", err)
	}
	return nil
}

func (r *Repository) UpdateMachineTelemetryCursor(ctx context.Context, machineID string, observedAt time.Time) error {
	tx := transaction.FromContext(ctx)
	if tx == nil {
		return errTransactionRequired
	}
	tag, err := tx.Exec(ctx, `UPDATE machines SET last_telemetry_observed_at=$2 WHERE id=$1`,
		machineID, observedAt.UTC().Truncate(time.Microsecond))
	if err != nil {
		return fmt.Errorf("update machine telemetry cursor: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return machine.ErrNotFound
	}
	return nil
}

func (r *Repository) InsertTelemetryOutbox(ctx context.Context, event telemetry.Event) error {
	tx := transaction.FromContext(ctx)
	if tx == nil {
		return errTransactionRequired
	}
	observation := event.Observation
	observation.ObservedAt = observation.ObservedAt.UTC().Truncate(time.Microsecond)
	payload, err := json.Marshal(observation)
	if err != nil {
		return fmt.Errorf("encode telemetry outbox payload: %w", err)
	}
	_, err = tx.Exec(ctx, `INSERT INTO telemetry_outbox (device_id,event_id,machine_id,payload,created_at)
		VALUES ($1,$2,$3,$4,$5)`, event.DeviceID, event.EventID, event.MachineID, payload, event.ReceivedAt)
	if err != nil {
		return telemetryInsertError("insert telemetry outbox", err)
	}
	return nil
}

func telemetryInsertError(operation string, err error) error {
	if pgErr, ok := errors.AsType[*pgconn.PgError](err); ok && pgErr.Code == pgerrcode.UniqueViolation {
		return fmt.Errorf("%s: %w: %w", operation, telemetry.ErrEventConflict, err)
	}
	return fmt.Errorf("%s: %w", operation, err)
}
