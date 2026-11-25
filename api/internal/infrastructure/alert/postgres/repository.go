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

func (r *Repository) Acknowledge(ctx context.Context, ownerID, alertID string) (bool, error) {
	var owned, changed bool
	// Lock before testing acknowledgement so concurrent retries see the winning update.
	err := r.pool.QueryRow(ctx, `WITH owned AS (
		SELECT a.id,a.acknowledged_at FROM machine_alerts a JOIN machines m ON m.id=a.machine_id
		WHERE m.owner_user_id=$1 AND a.id=$2 FOR UPDATE OF a
	), acknowledged AS (
		UPDATE machine_alerts a SET acknowledged_at=clock_timestamp()
		FROM owned WHERE a.id=owned.id AND owned.acknowledged_at IS NULL RETURNING 1
	)
	SELECT EXISTS (SELECT FROM owned), EXISTS (SELECT FROM acknowledged)`, ownerID, alertID).Scan(&owned, &changed)
	if err != nil {
		return false, fmt.Errorf("acknowledge owner machine alert: %w", err)
	}
	if !owned {
		return false, alert.ErrNotFound
	}
	return changed, nil
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
