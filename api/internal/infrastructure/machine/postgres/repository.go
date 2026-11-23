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
	rows, err := query(ctx, `SELECT id::text,owner_user_id::text,name,created_at
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
	value, err := scanMachine(queryRow(ctx, `SELECT id::text,owner_user_id::text,name,created_at
		FROM machines WHERE owner_user_id=$1 AND id=$2`, ownerUserID, machineID))
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
	if err := row.Scan(&value.ID, &value.OwnerUserID, &value.Name, &value.CreatedAt); err != nil {
		return machine.Machine{}, err
	}
	value.CreatedAt = value.CreatedAt.UTC()
	return value, nil
}
