package postgres

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/vladgrskkh/machine-whisperer/api/internal/domain/alert"
	"github.com/vladgrskkh/machine-whisperer/api/internal/domain/machine"
)

type Repository struct{ pool *pgxpool.Pool }

func New(pool *pgxpool.Pool) *Repository { return &Repository{pool: pool} }

func (r *Repository) ListByOwner(ctx context.Context, ownerID, machineID string) ([]alert.Alert, error) {
	rows, err := r.pool.Query(ctx, `SELECT
		a.id::text,a.machine_id::text,a.trigger_device_id::text,a.trigger_event_id::text,a.triggered_at,
		a.trigger_z_score,a.trigger_baseline_version,a.trigger_mean_rms_g,a.trigger_stddev_rms_g,
		a.last_z_score,a.peak_z_score,a.recovery_device_id::text,a.recovery_event_id::text,
		a.recovered_at,a.recovery_baseline_version,a.acknowledged_at
		FROM machine_alerts a JOIN machines m ON m.id=a.machine_id
		WHERE m.owner_user_id=$1 AND m.id=$2 ORDER BY a.triggered_at DESC,a.id DESC`, ownerID, machineID)
	if err != nil {
		return nil, fmt.Errorf("list owner machine alerts: %w", err)
	}
	values, err := pgx.CollectRows(rows, scanAlert)
	if err != nil {
		return nil, fmt.Errorf("read owner machine alerts: %w", err)
	}
	if len(values) == 0 {
		var owned bool
		err = r.pool.QueryRow(ctx, `SELECT EXISTS (
			SELECT 1 FROM machines WHERE owner_user_id=$1 AND id=$2
		)`, ownerID, machineID).Scan(&owned)
		if err != nil {
			return nil, fmt.Errorf("check alert history machine ownership: %w", err)
		}
		if !owned {
			return nil, machine.ErrNotFound
		}
	}
	return values, nil
}

func (r *Repository) Acknowledge(ctx context.Context, ownerID, alertID string) error {
	tag, err := r.pool.Exec(ctx, `UPDATE machine_alerts a
		SET acknowledged_at=COALESCE(a.acknowledged_at,clock_timestamp())
		FROM machines m WHERE m.id=a.machine_id AND m.owner_user_id=$1 AND a.id=$2`, ownerID, alertID)
	if err != nil {
		return fmt.Errorf("acknowledge owner machine alert: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return alert.ErrNotFound
	}
	return nil
}

func scanAlert(row pgx.CollectableRow) (alert.Alert, error) {
	var value alert.Alert
	if err := row.Scan(
		&value.ID, &value.MachineID, &value.TriggerDeviceID, &value.TriggerEventID, &value.TriggeredAt,
		&value.TriggerZScore, &value.TriggerBaselineVersion, &value.TriggerMeanRMSG, &value.TriggerStddevRMSG,
		&value.LastZScore, &value.PeakZScore, &value.RecoveryDeviceID, &value.RecoveryEventID,
		&value.RecoveredAt, &value.RecoveryBaselineVersion, &value.AcknowledgedAt,
	); err != nil {
		return alert.Alert{}, err
	}
	value.TriggeredAt = value.TriggeredAt.UTC()
	if value.RecoveredAt != nil {
		*value.RecoveredAt = value.RecoveredAt.UTC()
	}
	if value.AcknowledgedAt != nil {
		*value.AcknowledgedAt = value.AcknowledgedAt.UTC()
	}
	return value, nil
}
