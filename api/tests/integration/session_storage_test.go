//go:build integration

package integration_test

import (
	"context"
	"crypto/sha256"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgerrcode"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vladgrskkh/machine-whisperer/api/internal/domain/session"
	sessionpostgres "github.com/vladgrskkh/machine-whisperer/api/internal/infrastructure/session/postgres"
	transactionpostgres "github.com/vladgrskkh/machine-whisperer/api/internal/infrastructure/transaction/postgres"
	userpostgres "github.com/vladgrskkh/machine-whisperer/api/internal/infrastructure/user/postgres"
	"github.com/vladgrskkh/machine-whisperer/api/internal/testutil"
)

//nolint:tparallel // Subtests exercise the shared session lifecycle in order.
func TestSessionStorage(t *testing.T) {
	t.Parallel()
	pool := testutil.Postgres(t)
	repository := sessionpostgres.New(pool)
	userRepo := userpostgres.New(pool)
	manager := transactionpostgres.New(pool)
	created, err := userRepo.Create(t.Context(), "session@example.com", "hash")
	require.NoError(t, err)
	now := time.Now().UTC().Truncate(time.Microsecond)
	first := session.Session{
		ID:        uuid.NewString(),
		UserID:    created.ID,
		CreatedAt: now,
		ExpiresAt: now.Add(30 * 24 * time.Hour),
	}
	second := first
	second.ID = uuid.NewString()
	refresh := session.RefreshToken{Hash: sha256.Sum256([]byte("secret one")), SessionID: first.ID, CreatedAt: now}
	require.NoError(t, manager.WithinTransaction(t.Context(), func(ctx context.Context) error {
		if insertErr := repository.InsertSession(ctx, first); insertErr != nil {
			return insertErr
		}
		return repository.InsertRefresh(ctx, refresh)
	}))
	require.NoError(t, repository.InsertSession(t.Context(), second))
	var sessionCount int
	require.NoError(
		t,
		pool.QueryRow(t.Context(), "SELECT count(*) FROM sessions WHERE user_id=$1", created.ID).Scan(&sessionCount),
	)
	assert.Equal(t, 2, sessionCount)
	var actual session.Session
	require.NoError(
		t,
		pool.QueryRow(t.Context(), "SELECT id::text,user_id::text,created_at,expires_at,revoked_at FROM sessions WHERE id=$1", first.ID).
			Scan(&actual.ID, &actual.UserID, &actual.CreatedAt, &actual.ExpiresAt, &actual.RevokedAt),
	)
	assert.True(t, first.CreatedAt.Equal(actual.CreatedAt))
	assert.True(t, first.ExpiresAt.Equal(actual.ExpiresAt))
	assert.Nil(t, actual.RevokedAt)
	var storedHash []byte
	var consumed *time.Time
	require.NoError(
		t,
		pool.QueryRow(t.Context(), "SELECT hash,consumed_at FROM refresh_tokens WHERE session_id=$1", first.ID).
			Scan(&storedHash, &consumed),
	)
	assert.Equal(t, refresh.Hash[:], storedHash)
	assert.Nil(t, consumed)

	t.Run("second insert failure rolls back", func(t *testing.T) {
		rolledBack := first
		rolledBack.ID = uuid.NewString()
		err = manager.WithinTransaction(t.Context(), func(ctx context.Context) error {
			if insertErr := repository.InsertSession(ctx, rolledBack); insertErr != nil {
				return insertErr
			}
			duplicate := refresh
			duplicate.SessionID = rolledBack.ID
			return repository.InsertRefresh(ctx, duplicate)
		})
		requirePGCode(t, err, pgerrcode.UniqueViolation)
		assertSessionAbsent(t, pool, rolledBack.ID)
	})
	t.Run("cancellation rolls back user and session", func(t *testing.T) { testSessionCancellation(t, pool, first) })
	t.Run("canceled callback success cannot commit", func(t *testing.T) {
		rolledBack := first
		rolledBack.ID = uuid.NewString()
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		err = manager.WithinTransaction(ctx, func(txCtx context.Context) error {
			if insertErr := repository.InsertSession(txCtx, rolledBack); insertErr != nil {
				return insertErr
			}
			cancel()
			return nil
		})
		assert.ErrorIs(t, err, context.Canceled)
		assertSessionAbsent(t, pool, rolledBack.ID)
	})
	t.Run("callback error rolls back both tables", func(t *testing.T) {
		rolledBack := first
		rolledBack.ID = uuid.NewString()
		expected := errors.New("abort operation")
		err = manager.WithinTransaction(t.Context(), func(ctx context.Context) error {
			if insertErr := repository.InsertSession(ctx, rolledBack); insertErr != nil {
				return insertErr
			}
			value := refresh
			value.SessionID = rolledBack.ID
			value.Hash = sha256.Sum256([]byte("rollback secret"))
			if insertErr := repository.InsertRefresh(ctx, value); insertErr != nil {
				return insertErr
			}
			return expected
		})
		assert.ErrorIs(t, err, expected)
		assertSessionAbsent(t, pool, rolledBack.ID)
		var count int
		require.NoError(
			t,
			pool.QueryRow(t.Context(), "SELECT count(*) FROM refresh_tokens WHERE session_id=$1", rolledBack.ID).
				Scan(&count),
		)
		assert.Zero(t, count)
	})
	t.Run("deferred constraint commit failure rolls back", func(t *testing.T) {
		_, createErr := pool.Exec(
			t.Context(),
			"CREATE TABLE commit_probe (value integer UNIQUE DEFERRABLE INITIALLY DEFERRED)",
		)
		require.NoError(t, createErr)
		rolledBack := first
		rolledBack.ID = uuid.NewString()
		err = manager.WithinTransaction(t.Context(), func(ctx context.Context) error {
			if insertErr := repository.InsertSession(ctx, rolledBack); insertErr != nil {
				return insertErr
			}
			_, insertErr := transactionpostgres.FromContext(ctx).
				Exec(ctx, "INSERT INTO commit_probe (value) VALUES (1),(1)")
			return insertErr
		})
		requirePGCode(t, err, pgerrcode.UniqueViolation)
		assertSessionAbsent(t, pool, rolledBack.ID)
	})
	t.Run("constraints", func(t *testing.T) { testSessionConstraints(t, pool, first, refresh) })
	t.Run("session delete cascades", func(t *testing.T) {
		_, err = pool.Exec(t.Context(), "DELETE FROM sessions WHERE id=$1", first.ID)
		require.NoError(t, err)
		var count int
		require.NoError(
			t,
			pool.QueryRow(t.Context(), "SELECT count(*) FROM refresh_tokens WHERE session_id=$1", first.ID).
				Scan(&count),
		)
		assert.Zero(t, count)
	})
	t.Run("user delete cascades", func(t *testing.T) {
		remaining := refresh
		remaining.SessionID = second.ID
		require.NoError(t, repository.InsertRefresh(t.Context(), remaining))
		_, err = pool.Exec(t.Context(), "DELETE FROM users WHERE id=$1", created.ID)
		require.NoError(t, err)
		assertSessionAbsent(t, pool, second.ID)
		var count int
		require.NoError(
			t,
			pool.QueryRow(t.Context(), "SELECT count(*) FROM refresh_tokens WHERE session_id=$1", second.ID).
				Scan(&count),
		)
		assert.Zero(t, count)
	})
}

func testSessionConstraints(t *testing.T, pool *pgxpool.Pool, first session.Session, refresh session.RefreshToken) {
	t.Helper()
	for _, tt := range []struct {
		name, query, code string
		args              []any
	}{
		{"session UUID", "INSERT INTO sessions (id,user_id,created_at,expires_at) VALUES ($1,$2,$3,$4)", pgerrcode.InvalidTextRepresentation, []any{"invalid", first.UserID, first.CreatedAt, first.ExpiresAt}},
		{"session FK", "INSERT INTO sessions (id,user_id,created_at,expires_at) VALUES ($1,$2,$3,$4)", pgerrcode.ForeignKeyViolation, []any{uuid.NewString(), uuid.NewString(), first.CreatedAt, first.ExpiresAt}},
		{"session unique", "INSERT INTO sessions (id,user_id,created_at,expires_at) VALUES ($1,$2,$3,$4)", pgerrcode.UniqueViolation, []any{first.ID, first.UserID, first.CreatedAt, first.ExpiresAt}},
		{"refresh FK", "INSERT INTO refresh_tokens (hash,session_id,created_at) VALUES ($1,$2,$3)", pgerrcode.ForeignKeyViolation, []any{sha256.New().Sum(nil), uuid.NewString(), first.CreatedAt}},
		{"refresh hash length", "INSERT INTO refresh_tokens (hash,session_id,created_at) VALUES ($1,$2,$3)", pgerrcode.CheckViolation, []any{[]byte("short"), first.ID, first.CreatedAt}},
		{"refresh unique", "INSERT INTO refresh_tokens (hash,session_id,created_at) VALUES ($1,$2,$3)", pgerrcode.UniqueViolation, []any{refresh.Hash[:], first.ID, first.CreatedAt}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			_, err := pool.Exec(t.Context(), tt.query, tt.args...)
			requirePGCode(t, err, tt.code)
		})
	}
}

func requirePGCode(t *testing.T, err error, code string) {
	t.Helper()
	var pgErr *pgconn.PgError
	require.ErrorAs(t, err, &pgErr)
	assert.Equal(t, code, pgErr.Code)
}

func assertSessionAbsent(t *testing.T, pool *pgxpool.Pool, id string) {
	t.Helper()
	var count int
	require.NoError(t, pool.QueryRow(t.Context(), "SELECT count(*) FROM sessions WHERE id=$1", id).Scan(&count))
	assert.Zero(t, count)
}

func testSessionCancellation(t *testing.T, pool *pgxpool.Pool, first session.Session) {
	t.Helper()
	repository := sessionpostgres.New(pool)
	userRepo := userpostgres.New(pool)
	manager := transactionpostgres.New(pool)

	rolledBack := first
	rolledBack.ID = uuid.NewString()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	err := manager.WithinTransaction(ctx, func(txCtx context.Context) error {
		added, createErr := userRepo.Create(txCtx, "rollback@example.com", "hash")
		if createErr != nil {
			return createErr
		}
		rolledBack.UserID = added.ID
		if insertErr := repository.InsertSession(txCtx, rolledBack); insertErr != nil {
			return insertErr
		}
		cancel()
		return context.Canceled
	})
	assert.ErrorIs(t, err, context.Canceled)
	assertSessionAbsent(t, pool, rolledBack.ID)
	var count int
	require.NoError(
		t,
		pool.QueryRow(t.Context(), "SELECT count(*) FROM users WHERE email='rollback@example.com'").Scan(&count),
	)
	assert.Zero(t, count)
}
