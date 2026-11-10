package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/vladgrskkh/machine-whisperer/api/internal/domain/user"
)

type Repository struct{ pool *pgxpool.Pool }

func New(pool *pgxpool.Pool) *Repository { return &Repository{pool: pool} }

func (r *Repository) Create(ctx context.Context, email, passwordHash string) (user.User, error) {
	created := user.User{}
	err := r.pool.QueryRow(ctx, `INSERT INTO users (id,email,password_hash) VALUES ($1,$2,$3) RETURNING id::text,email,created_at`, uuid.NewString(), email, passwordHash).
		Scan(&created.ID, &created.Email, &created.CreatedAt)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" && pgErr.ConstraintName == "users_email_key" {
			return user.User{}, user.ErrEmailExists
		}
		return user.User{}, fmt.Errorf("insert user: %w", err)
	}
	created.CreatedAt = created.CreatedAt.UTC()
	return created, nil
}
