package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgerrcode"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/vladgrskkh/machine-whisperer/api/internal/domain/machine"
	transaction "github.com/vladgrskkh/machine-whisperer/api/internal/infrastructure/transaction/postgres"
)

var errTransactionRequired = errors.New("machine baseline write requires a transaction")

type Repository struct{ pool *pgxpool.Pool }

func New(pool *pgxpool.Pool) *Repository { return &Repository{pool: pool} }

func (r *Repository) Create(ctx context.Context, value machine.Machine) error {
	exec := r.pool.Exec
	if tx := transaction.FromContext(ctx); tx != nil {
		exec = tx.Exec
	}
	_, err := exec(ctx, `INSERT INTO machines (id,owner_user_id,name,created_at) VALUES ($1,$2,$3,$4)`,
		value.ID, value.OwnerUserID, value.Name, value.CreatedAt)
	if pgErr, ok := errors.AsType[*pgconn.PgError](err); ok {
		switch pgErr.Code {
		case pgerrcode.UniqueViolation:
			return machine.ErrConflict
		case pgerrcode.ForeignKeyViolation:
			return machine.ErrNotFound
		}
	}
	if err != nil {
		return fmt.Errorf("insert machine: %w", err)
	}
	return nil
}

func (r *Repository) ListByOwner(ctx context.Context, ownerUserID string) ([]machine.Machine, error) {
	query := r.pool.Query
	if tx := transaction.FromContext(ctx); tx != nil {
		query = tx.Query
	}
	rows, err := query(ctx, `SELECT id::text,owner_user_id::text,name,created_at,last_telemetry_observed_at
		FROM machines WHERE owner_user_id=$1 ORDER BY created_at DESC,id DESC`, ownerUserID)
	if err != nil {
		return nil, fmt.Errorf("list owner machines: %w", err)
	}
	values, err := pgx.AppendRows([]machine.Machine{}, rows, func(row pgx.CollectableRow) (machine.Machine, error) {
		return scanMachine(row)
	})
	if err != nil {
		return nil, fmt.Errorf("read owner machines: %w", err)
	}
	return values, nil
}

func (r *Repository) FindByOwner(ctx context.Context, ownerUserID, machineID string) (machine.Machine, error) {
	queryRow := r.pool.QueryRow
	if tx := transaction.FromContext(ctx); tx != nil {
		queryRow = tx.QueryRow
	}
	value, err := scanMachine(
		queryRow(ctx, `SELECT id::text,owner_user_id::text,name,created_at,last_telemetry_observed_at
		FROM machines WHERE owner_user_id=$1 AND id=$2`, ownerUserID, machineID),
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return machine.Machine{}, machine.ErrNotFound
	}
	if err != nil {
		return machine.Machine{}, fmt.Errorf("find owner machine: %w", err)
	}
	return value, nil
}

func scanMachine(row pgx.Row) (machine.Machine, error) {
	var value machine.Machine
	if err := row.Scan(
		&value.ID,
		&value.OwnerUserID,
		&value.Name,
		&value.CreatedAt,
		&value.LastTelemetryObservedAt,
	); err != nil {
		return machine.Machine{}, err
	}
	value.CreatedAt = value.CreatedAt.UTC()
	if value.LastTelemetryObservedAt != nil {
		*value.LastTelemetryObservedAt = value.LastTelemetryObservedAt.UTC()
	}
	return value, nil
}

func (r *Repository) GetByOwner(ctx context.Context, ownerID, machineID string) (machine.Baseline, error) {
	queryRow := r.pool.QueryRow
	if tx := transaction.FromContext(ctx); tx != nil {
		queryRow = tx.QueryRow
	}
	baseline, err := scanBaseline(
		queryRow(ctx, `SELECT b.machine_id::text,b.mean_rms_g,b.stddev_rms_g,b.version,b.updated_at
		FROM machine_baselines b JOIN machines m ON m.id=b.machine_id
		WHERE m.owner_user_id=$1 AND m.id=$2`, ownerID, machineID),
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return machine.Baseline{}, machine.ErrBaselineNotFound
	}
	if err != nil {
		return machine.Baseline{}, fmt.Errorf("read owner machine baseline: %w", err)
	}
	return baseline, nil
}

func (r *Repository) SetByOwner(
	ctx context.Context, ownerID, machineID string, baseline machine.Baseline,
) (machine.Baseline, error) {
	tx := transaction.FromContext(ctx)
	if tx == nil {
		return machine.Baseline{}, errTransactionRequired
	}

	// Serialize baseline changes with telemetry ingestion on the machine row.
	var lockedID string
	err := tx.QueryRow(ctx, `SELECT id::text FROM machines WHERE owner_user_id=$1 AND id=$2 FOR UPDATE`,
		ownerID, machineID).Scan(&lockedID)
	if errors.Is(err, pgx.ErrNoRows) {
		return machine.Baseline{}, machine.ErrNotFound
	}
	if err != nil {
		return machine.Baseline{}, fmt.Errorf("lock owner machine for baseline write: %w", err)
	}

	stored, err := scanBaseline(tx.QueryRow(ctx, `INSERT INTO machine_baselines (machine_id,mean_rms_g,stddev_rms_g)
		VALUES ($1,$2,$3)
		ON CONFLICT (machine_id) DO UPDATE
			SET mean_rms_g=EXCLUDED.mean_rms_g,stddev_rms_g=EXCLUDED.stddev_rms_g,
				version=machine_baselines.version+1,updated_at=clock_timestamp()
			WHERE (machine_baselines.mean_rms_g,machine_baselines.stddev_rms_g)
				IS DISTINCT FROM (EXCLUDED.mean_rms_g,EXCLUDED.stddev_rms_g)
		RETURNING machine_id::text,mean_rms_g,stddev_rms_g,version,updated_at`,
		lockedID, baseline.MeanRMSG, baseline.StddevRMSG))
	if errors.Is(err, pgx.ErrNoRows) {
		// An identical PUT leaves the stored version and timestamp untouched.
		return r.GetByOwner(ctx, ownerID, lockedID)
	}
	if err != nil {
		return machine.Baseline{}, fmt.Errorf("write owner machine baseline: %w", err)
	}
	return stored, nil
}

func scanBaseline(row pgx.Row) (machine.Baseline, error) {
	var value machine.Baseline
	if err := row.Scan(
		&value.MachineID,
		&value.MeanRMSG,
		&value.StddevRMSG,
		&value.Version,
		&value.UpdatedAt,
	); err != nil {
		return machine.Baseline{}, err
	}
	value.UpdatedAt = value.UpdatedAt.UTC()
	return value, nil
}
