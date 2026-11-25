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

	"github.com/vladgrskkh/machine-whisperer/api/internal/domain/alert"
	"github.com/vladgrskkh/machine-whisperer/api/internal/domain/machine"
	"github.com/vladgrskkh/machine-whisperer/api/internal/domain/telemetry"
	transaction "github.com/vladgrskkh/machine-whisperer/api/internal/infrastructure/transaction/postgres"
	"github.com/vladgrskkh/machine-whisperer/api/internal/spanutil"
	telemetryusecase "github.com/vladgrskkh/machine-whisperer/api/internal/usecase/telemetry"
)

var errTransactionRequired = errors.New("telemetry operation requires a transaction")

type Repository struct{}

func New() *Repository { return &Repository{} }

func (r *Repository) LockActiveDeviceForTelemetry(ctx context.Context, deviceID, machineID string) error {
	tx := transaction.FromContext(ctx)
	if tx == nil {
		return errTransactionRequired
	}

	// Revocation updates non-key fields, so this must conflict with non-key row updates.
	var id string
	err := tx.QueryRow(ctx, `SELECT id::text FROM devices
		WHERE id=$1 AND machine_id=$2 AND revoked_at IS NULL FOR SHARE`, deviceID, machineID).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return machine.ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("lock active device for telemetry: %w", err)
	}
	return nil
}

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

func (r *Repository) DeleteExpiredTelemetryEvents(
	ctx context.Context,
	machineID, deviceID, eventID string,
	dbNow time.Time,
) error {
	tx := transaction.FromContext(ctx)
	if tx == nil {
		return errTransactionRequired
	}
	// Check the submitted key separately so it can expire even when the batch fills up.
	// Tuple IDs let PostgreSQL delete the selected rows directly.
	_, err := tx.Exec(ctx, `DELETE FROM telemetry_events WHERE ctid = ANY(ARRAY(
		SELECT ctid FROM telemetry_events WHERE device_id=$2 AND event_id=$3 AND expires_at <= $4
		UNION ALL
		(SELECT ctid FROM telemetry_events
		 WHERE machine_id=$1 AND expires_at <= $4 AND (device_id,event_id) <> ($2,$3)
		 ORDER BY expires_at LIMIT 100)
	))`, machineID, deviceID, eventID, dbNow)
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
	_, err = tx.Exec(
		ctx,
		`INSERT INTO telemetry_outbox (device_id,event_id,machine_id,payload,created_at,traceparent)
		VALUES ($1,$2,$3,$4,$5,NULLIF($6,''))`,
		event.DeviceID,
		event.EventID,
		event.MachineID,
		payload,
		event.ReceivedAt,
		spanutil.SampledLocalTraceparent(ctx),
	)
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

func (r *Repository) Load(ctx context.Context, machineID string) (
	machine.Baseline, telemetryusecase.DetectorState, bool, error,
) {
	tx := transaction.FromContext(ctx)
	if tx == nil {
		return machine.Baseline{}, telemetryusecase.DetectorState{}, false, errTransactionRequired
	}
	var baseline machine.Baseline
	var state telemetryusecase.DetectorState
	var activeID *string
	var peak *float64
	err := tx.QueryRow(ctx, `SELECT b.machine_id::text,b.mean_rms_g,b.stddev_rms_g,b.version,b.updated_at,
		COALESCE(s.baseline_version,0),COALESCE(s.pending_alert_count,0),COALESCE(s.pending_recovery_count,0),
		a.id::text,a.peak_z_score
		FROM machine_baselines b
		LEFT JOIN machine_detector_state s ON s.machine_id=b.machine_id
		LEFT JOIN machine_alerts a ON a.machine_id=b.machine_id AND a.recovered_at IS NULL
		WHERE b.machine_id=$1`, machineID).Scan(
		&baseline.MachineID, &baseline.MeanRMSG, &baseline.StddevRMSG, &baseline.Version, &baseline.UpdatedAt,
		&state.BaselineVersion, &state.PendingAlertCount, &state.PendingRecoveryCount, &activeID, &peak,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return machine.Baseline{}, telemetryusecase.DetectorState{}, false, nil
	}
	if err != nil {
		return machine.Baseline{}, telemetryusecase.DetectorState{}, false, fmt.Errorf("load detector state: %w", err)
	}
	baseline.UpdatedAt = baseline.UpdatedAt.UTC()
	if activeID != nil {
		state.Active = &telemetryusecase.ActiveAlert{ID: *activeID, PeakZScore: *peak}
	}
	return baseline, state, true, nil
}

func (r *Repository) SaveTransition(
	ctx context.Context, machineID string, state telemetryusecase.DetectorState, changedAlert *alert.Alert,
) error {
	tx := transaction.FromContext(ctx)
	if tx == nil {
		return errTransactionRequired
	}
	if _, err := tx.Exec(ctx, `INSERT INTO machine_detector_state (
			machine_id,baseline_version,pending_alert_count,pending_recovery_count)
		VALUES ($1,$2,$3,$4) ON CONFLICT (machine_id) DO UPDATE SET
			baseline_version=EXCLUDED.baseline_version,pending_alert_count=EXCLUDED.pending_alert_count,
			pending_recovery_count=EXCLUDED.pending_recovery_count
		WHERE (machine_detector_state.baseline_version,machine_detector_state.pending_alert_count,
			machine_detector_state.pending_recovery_count) IS DISTINCT FROM
			(EXCLUDED.baseline_version,EXCLUDED.pending_alert_count,EXCLUDED.pending_recovery_count)`,
		machineID, state.BaselineVersion, state.PendingAlertCount, state.PendingRecoveryCount); err != nil {
		return fmt.Errorf("save detector state: %w", err)
	}
	if changedAlert == nil {
		return nil
	}
	if changedAlert.ID == "" {
		return insertAlert(ctx, tx, changedAlert)
	}
	return updateAlert(ctx, tx, changedAlert)
}

func insertAlert(ctx context.Context, tx pgx.Tx, value *alert.Alert) error {
	_, err := tx.Exec(
		ctx,
		`INSERT INTO machine_alerts (
		machine_id,trigger_device_id,trigger_event_id,triggered_at,trigger_z_score,trigger_baseline_version,
		trigger_mean_rms_g,trigger_stddev_rms_g,last_z_score,peak_z_score)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`,
		value.MachineID,
		value.TriggerDeviceID,
		value.TriggerEventID,
		value.TriggeredAt,
		value.TriggerZScore,
		value.TriggerBaselineVersion,
		value.TriggerMeanRMSG,
		value.TriggerStddevRMSG,
		value.LastZScore,
		value.PeakZScore,
	)
	if err != nil {
		return fmt.Errorf("insert machine alert: %w", err)
	}
	return nil
}

func updateAlert(ctx context.Context, tx pgx.Tx, value *alert.Alert) error {
	tag, err := tx.Exec(ctx, `UPDATE machine_alerts SET last_z_score=$3,peak_z_score=$4,
		recovery_device_id=$5,recovery_event_id=$6,recovered_at=$7,recovery_baseline_version=$8
		WHERE machine_id=$1 AND id=$2 AND recovered_at IS NULL`,
		value.MachineID, value.ID, value.LastZScore, value.PeakZScore,
		value.RecoveryDeviceID, value.RecoveryEventID, value.RecoveredAt, value.RecoveryBaselineVersion,
	)
	if err != nil {
		return fmt.Errorf("update machine alert: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return alert.ErrNotFound
	}
	return nil
}
