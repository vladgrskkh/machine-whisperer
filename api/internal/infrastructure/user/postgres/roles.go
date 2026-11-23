package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgerrcode"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/vladgrskkh/machine-whisperer/api/internal/domain/user"
	transaction "github.com/vladgrskkh/machine-whisperer/api/internal/infrastructure/transaction/postgres"
)

func (r *Repository) ListRoles(ctx context.Context, userID string) ([]string, error) {
	queryRow := r.pool.QueryRow
	if tx := transaction.FromContext(ctx); tx != nil {
		queryRow = tx.QueryRow
	}
	var roles []string
	err := queryRow(ctx, `SELECT ARRAY(SELECT role FROM user_roles WHERE user_id=$1 ORDER BY role)`, userID).
		Scan(&roles)
	if err != nil {
		return nil, fmt.Errorf("list user roles: %w", err)
	}
	return roles, nil
}

func (r *Repository) GrantRole(ctx context.Context, userID, role string) error {
	exec := r.pool.Exec
	if tx := transaction.FromContext(ctx); tx != nil {
		exec = tx.Exec
	}
	_, err := exec(ctx, `INSERT INTO user_roles (user_id,role) VALUES ($1,$2) ON CONFLICT (user_id,role) DO NOTHING`,
		userID, role)
	if err != nil {
		if pgErr, ok := errors.AsType[*pgconn.PgError](err); ok && pgErr.Code == pgerrcode.ForeignKeyViolation {
			return user.ErrNotFound
		}
		return fmt.Errorf("grant user role: %w", err)
	}
	return nil
}

func (r *Repository) RevokeRole(ctx context.Context, userID, role string) error {
	exec := r.pool.Exec
	if tx := transaction.FromContext(ctx); tx != nil {
		exec = tx.Exec
	}
	if _, err := exec(ctx, `DELETE FROM user_roles WHERE user_id=$1 AND role=$2`, userID, role); err != nil {
		return fmt.Errorf("revoke user role: %w", err)
	}
	return nil
}
