package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgerrcode"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/vladgrskkh/machine-whisperer/api/internal/domain/user"
	transaction "github.com/vladgrskkh/machine-whisperer/api/internal/infrastructure/transaction/postgres"
)

type Repository struct{ pool *pgxpool.Pool }

func New(pool *pgxpool.Pool) *Repository { return &Repository{pool: pool} }

func (r *Repository) Create(ctx context.Context, email, passwordHash string) (user.User, error) {
	created := user.User{}
	queryRow := r.pool.QueryRow
	if tx := transaction.FromContext(ctx); tx != nil {
		queryRow = tx.QueryRow
	}
	err := queryRow(
		ctx,
		`INSERT INTO users (id,email,password_hash) VALUES ($1,$2,$3) RETURNING id::text,email,created_at`,
		uuid.NewString(),
		email,
		passwordHash,
	).
		Scan(&created.ID, &created.Email, &created.CreatedAt)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == pgerrcode.UniqueViolation &&
			pgErr.ConstraintName == "users_email_key" {
			return user.User{}, user.ErrEmailExists
		}
		return user.User{}, fmt.Errorf("insert user: %w", err)
	}
	created.CreatedAt = created.CreatedAt.UTC()
	return created, nil
}

func (r *Repository) FindCredentialsByEmail(ctx context.Context, email string) (user.User, string, error) {
	account := user.User{}
	var hash string
	queryRow := r.pool.QueryRow
	if tx := transaction.FromContext(ctx); tx != nil {
		queryRow = tx.QueryRow
	}
	err := queryRow(ctx, `SELECT id::text,email,created_at,password_hash FROM users WHERE email=$1`, email).
		Scan(&account.ID, &account.Email, &account.CreatedAt, &hash)
	if errors.Is(err, pgx.ErrNoRows) {
		return user.User{}, "", user.ErrNotFound
	}
	if err != nil {
		return user.User{}, "", fmt.Errorf("find user credentials: %w", err)
	}
	account.CreatedAt = account.CreatedAt.UTC()
	return account, hash, nil
}

func (r *Repository) FindByID(ctx context.Context, id string) (user.User, error) {
	account := user.User{}
	queryRow := r.pool.QueryRow
	if tx := transaction.FromContext(ctx); tx != nil {
		queryRow = tx.QueryRow
	}
	err := queryRow(ctx, `SELECT id::text,email,created_at FROM users WHERE id=$1`, id).
		Scan(&account.ID, &account.Email, &account.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return user.User{}, user.ErrNotFound
	}
	if err != nil {
		return user.User{}, fmt.Errorf("find user: %w", err)
	}
	account.CreatedAt = account.CreatedAt.UTC()
	return account, nil
}
