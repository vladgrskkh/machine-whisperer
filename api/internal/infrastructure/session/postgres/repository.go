package postgres

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/vladgrskkh/machine-whisperer/api/internal/domain/session"
	transaction "github.com/vladgrskkh/machine-whisperer/api/internal/infrastructure/transaction/postgres"
)

type Repository struct{ pool *pgxpool.Pool }

func New(pool *pgxpool.Pool) *Repository { return &Repository{pool: pool} }

func (r *Repository) InsertSession(ctx context.Context, value session.Session) error {
	exec := r.pool.Exec
	if tx := transaction.FromContext(ctx); tx != nil {
		exec = tx.Exec
	}
	_, err := exec(
		ctx,
		`INSERT INTO sessions (id,user_id,created_at,expires_at,revoked_at) VALUES ($1,$2,$3,$4,$5)`,
		value.ID,
		value.UserID,
		value.CreatedAt,
		value.ExpiresAt,
		value.RevokedAt,
	)
	if err != nil {
		return fmt.Errorf("insert session: %w", err)
	}
	return nil
}

func (r *Repository) InsertRefresh(ctx context.Context, value session.RefreshToken) error {
	exec := r.pool.Exec
	if tx := transaction.FromContext(ctx); tx != nil {
		exec = tx.Exec
	}
	_, err := exec(
		ctx,
		`INSERT INTO refresh_tokens (hash,session_id,created_at,consumed_at) VALUES ($1,$2,$3,$4)`,
		value.Hash[:],
		value.SessionID,
		value.CreatedAt,
		value.ConsumedAt,
	)
	if err != nil {
		return fmt.Errorf("insert refresh token: %w", err)
	}
	return nil
}
