package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/vladgrskkh/machine-whisperer/api/internal/domain/session"
	transaction "github.com/vladgrskkh/machine-whisperer/api/internal/infrastructure/transaction/postgres"
)

var (
	errTransactionRequired = errors.New("session operation requires a transaction")
	errUnexpectedRows      = errors.New("expected one updated row")
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

func (r *Repository) LockByRefreshHash(
	ctx context.Context,
	hash [32]byte,
) (session.Session, session.RefreshToken, error) {
	tx := transaction.FromContext(ctx)
	if tx == nil {
		return session.Session{}, session.RefreshToken{}, errTransactionRequired
	}

	// Resolve the immutable owner first without locking the token row.
	var id string
	err := tx.QueryRow(ctx, "SELECT session_id::text FROM refresh_tokens WHERE hash=$1", hash[:]).Scan(&id)
	if err != nil {
		return session.Session{}, session.RefreshToken{}, refreshLookupError("find refresh session", err)
	}

	var value session.Session
	err = tx.QueryRow(ctx, "SELECT id::text,user_id::text,created_at,expires_at,revoked_at FROM sessions WHERE id=$1 FOR UPDATE", id).
		Scan(&value.ID, &value.UserID, &value.CreatedAt, &value.ExpiresAt, &value.RevokedAt)
	if err != nil {
		return session.Session{}, session.RefreshToken{}, refreshLookupError("lock refresh session", err)
	}

	// Reread under the session lock to observe any preceding rotation.
	refresh := session.RefreshToken{Hash: hash, SessionID: id}
	err = tx.QueryRow(ctx, "SELECT created_at,consumed_at FROM refresh_tokens WHERE hash=$1 AND session_id=$2 FOR UPDATE", hash[:], id).
		Scan(&refresh.CreatedAt, &refresh.ConsumedAt)
	if err != nil {
		return session.Session{}, session.RefreshToken{}, refreshLookupError("lock refresh token", err)
	}
	return value, refresh, nil
}

func refreshLookupError(operation string, err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return session.ErrNotFound
	}
	return fmt.Errorf("%s: %w", operation, err)
}

func (r *Repository) ConsumeRefresh(ctx context.Context, hash [32]byte, at time.Time) error {
	tx := transaction.FromContext(ctx)
	if tx == nil {
		return errTransactionRequired
	}
	tag, err := tx.Exec(
		ctx,
		"UPDATE refresh_tokens SET consumed_at=$2 WHERE hash=$1 AND consumed_at IS NULL",
		hash[:],
		at,
	)
	if err != nil {
		return fmt.Errorf("consume refresh token: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return fmt.Errorf("consume refresh token: %w", errUnexpectedRows)
	}
	return nil
}

func (r *Repository) RevokeSession(ctx context.Context, sessionID string, at time.Time) error {
	tx := transaction.FromContext(ctx)
	if tx == nil {
		return errTransactionRequired
	}
	tag, err := tx.Exec(ctx, "UPDATE sessions SET revoked_at=$2 WHERE id=$1 AND revoked_at IS NULL", sessionID, at)
	if err != nil {
		return fmt.Errorf("revoke session: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return fmt.Errorf("revoke session: %w", errUnexpectedRows)
	}
	return nil
}
