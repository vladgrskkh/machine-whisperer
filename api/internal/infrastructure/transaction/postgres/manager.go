package postgres

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const rollbackTimeout = 5 * time.Second

type transactionKey struct{}

type Manager struct{ pool *pgxpool.Pool }

func New(pool *pgxpool.Pool) *Manager { return &Manager{pool: pool} }

func (m *Manager) WithinTransaction(ctx context.Context, fn func(context.Context) error) error {
	tx, err := m.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}
	defer func() {
		// Cancellation must not prevent releasing the connection and rolling back.
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), rollbackTimeout)
		defer cancel()
		_ = tx.Rollback(cleanupCtx)
	}()
	if err = fn(context.WithValue(ctx, transactionKey{}, tx)); err != nil {
		return err
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	if err = tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit transaction: %w", err)
	}
	return nil
}

// FromContext lets PostgreSQL adapters join the use case's transaction.
func FromContext(ctx context.Context) pgx.Tx {
	tx, _ := ctx.Value(transactionKey{}).(pgx.Tx)
	return tx
}
