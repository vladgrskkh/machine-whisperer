//go:build integration

package integration_test

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/vladgrskkh/machine-whisperer/api/internal/domain/session"
	"github.com/vladgrskkh/machine-whisperer/api/internal/domain/user"
	"github.com/vladgrskkh/machine-whisperer/api/internal/infrastructure/auth/attempts"
	"github.com/vladgrskkh/machine-whisperer/api/internal/infrastructure/auth/password"
	"github.com/vladgrskkh/machine-whisperer/api/internal/infrastructure/auth/token"
	sessionpostgres "github.com/vladgrskkh/machine-whisperer/api/internal/infrastructure/session/postgres"
	transactionpostgres "github.com/vladgrskkh/machine-whisperer/api/internal/infrastructure/transaction/postgres"
	userpostgres "github.com/vladgrskkh/machine-whisperer/api/internal/infrastructure/user/postgres"
	"github.com/vladgrskkh/machine-whisperer/api/internal/testutil"
	"github.com/vladgrskkh/machine-whisperer/api/internal/usecase/auth"
	"github.com/vladgrskkh/machine-whisperer/api/internal/usecase/auth/authmock"
)

const exactLoginPassword = "  exact\U0001f600 \ufffd \\ud800 password  "
const dummyLoginPassword = "fixed dummy password material"

func loginFixture(t *testing.T) (*auth.Usecase, *pgxpool.Pool, *token.JWT, time.Time, auth.LoginDependencies) {
	t.Helper()
	pool := testutil.Postgres(t)
	hasher := password.Argon2id{}
	dummy, err := hasher.Hash(t.Context(), dummyLoginPassword)
	require.NoError(t, err)
	public, private, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	now := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	signer, err := token.NewJWT(
		token.Keys{
			SigningKeyID: "active",
			SigningKey:   private,
			VerifyKeys:   map[string]ed25519.PublicKey{"active": public},
		},
		func() time.Time { return now },
	)
	require.NoError(t, err)
	clock := authmock.NewClock(t)
	clock.EXPECT().Now().Return(now).Maybe()
	dependencies := auth.LoginDependencies{
		Sessions:     sessionpostgres.New(pool),
		Transactions: transactionpostgres.New(pool),
		Signer:       signer,
		Refresh:      token.Refresh{},
		IDs:          token.IDs{},
		Clock:        clock,
		Attempts:     attempts.New(attempts.Config{Now: func() time.Time { return now }}),
		DummyHash:    dummy,
	}
	uc := auth.NewWithLogin(userpostgres.New(pool), hasher, dependencies)
	return uc, pool, signer, now, dependencies
}

func TestLogin(t *testing.T) {
	t.Parallel()
	uc, pool, signer, now, _ := loginFixture(t)
	original, err := uc.Signup(t.Context(), "alice@example.com", exactLoginPassword)
	require.NoError(t, err)
	first, err := uc.Login(t.Context(), "  Alice@Example.COM  ", exactLoginPassword)
	require.NoError(t, err)
	second, err := uc.Login(t.Context(), "alice@example.com", exactLoginPassword)
	require.NoError(t, err)
	assert.Equal(t, 900, first.ExpiresIn)
	assert.NotEmpty(t, first.AccessToken)
	assert.NotEmpty(t, first.RefreshToken)
	assert.NotEqual(t, first.AccessToken, second.AccessToken)
	assert.NotEqual(t, first.RefreshToken, second.RefreshToken)
	firstClaims, err := signer.Verify(t.Context(), first.AccessToken)
	require.NoError(t, err)
	secondClaims, err := signer.Verify(t.Context(), second.AccessToken)
	require.NoError(t, err)
	assert.Equal(t, original.ID, firstClaims.UserID)
	assert.NotEqual(t, firstClaims.SessionID, secondClaims.SessionID)
	assert.NotEqual(t, firstClaims.JTI, secondClaims.JTI)
	assert.Equal(t, now, firstClaims.IssuedAt)
	assert.Equal(t, now.Add(15*time.Minute), firstClaims.ExpiresAt)
	var stored session.Session
	require.NoError(
		t,
		pool.QueryRow(t.Context(), "SELECT id::text,user_id::text,created_at,expires_at,revoked_at FROM sessions WHERE id=$1", firstClaims.SessionID).
			Scan(&stored.ID, &stored.UserID, &stored.CreatedAt, &stored.ExpiresAt, &stored.RevokedAt),
	)
	assert.True(t, now.Equal(stored.CreatedAt))
	assert.True(t, now.Add(30*24*time.Hour).Equal(stored.ExpiresAt))
	assert.Nil(t, stored.RevokedAt)
	raw, err := base64.RawURLEncoding.DecodeString(first.RefreshToken)
	require.NoError(t, err)
	require.Len(t, raw, 32)
	hash := sha256.Sum256(raw)
	var storedHash []byte
	var refreshCreated time.Time
	require.NoError(
		t,
		pool.QueryRow(t.Context(), "SELECT hash,created_at FROM refresh_tokens WHERE session_id=$1", firstClaims.SessionID).
			Scan(&storedHash, &refreshCreated),
	)
	assert.Equal(t, hash[:], storedHash)
	assert.True(t, now.Equal(refreshCreated))
	_, err = pool.Exec(t.Context(), "UPDATE sessions SET revoked_at=$1 WHERE id=$2", now, firstClaims.SessionID)
	require.NoError(t, err)
	_, err = signer.Verify(t.Context(), first.AccessToken)
	require.NoError(t, err)
	current, err := uc.Me(t.Context(), original.ID)
	require.NoError(t, err)
	assert.Equal(t, original, current)
	for _, input := range []struct{ email, password string }{
		{"alice@example.com", "wrong password material"},
		{"missing@example.com", "wrong password material"},
		{"missing@example.com", dummyLoginPassword},
		{"alice@example.com", "exact\U0001f600 \ufffd \\ud800 password"},
	} {
		pair, loginErr := uc.Login(t.Context(), input.email, input.password)
		assert.ErrorIs(t, loginErr, auth.ErrAuthenticationFailed)
		assert.Empty(t, pair)
	}
	var count int
	require.NoError(t, pool.QueryRow(t.Context(), "SELECT count(*) FROM sessions").Scan(&count))
	assert.Equal(t, 2, count)
	var originalHash string
	require.NoError(
		t,
		pool.QueryRow(t.Context(), "SELECT password_hash FROM users WHERE id=$1", original.ID).Scan(&originalHash),
	)
	valid, err := (password.Argon2id{}).Verify(t.Context(), exactLoginPassword, originalHash)
	require.NoError(t, err)
	assert.True(t, valid)
}

func TestCurrentUser(t *testing.T) {
	t.Parallel()
	uc, pool, _, _, _ := loginFixture(t)
	original, err := uc.Signup(t.Context(), "me@example.com", exactLoginPassword)
	require.NoError(t, err)
	current, err := uc.Me(t.Context(), original.ID)
	require.NoError(t, err)
	assert.Equal(t, original, current)
	missing, err := uc.Me(t.Context(), uuid.NewString())
	assert.ErrorIs(t, err, auth.ErrAuthenticationFailed)
	assert.Empty(t, missing)
	_, err = pool.Exec(t.Context(), "DELETE FROM users WHERE id=$1", original.ID)
	require.NoError(t, err)
	missing, err = uc.Me(t.Context(), original.ID)
	assert.ErrorIs(t, err, auth.ErrAuthenticationFailed)
	assert.Empty(t, missing)
	_, err = pool.Exec(t.Context(), "DROP TABLE users CASCADE")
	require.NoError(t, err)
	missing, err = uc.Me(t.Context(), original.ID)
	assert.Error(t, err)
	assert.NotErrorIs(t, err, auth.ErrAuthenticationFailed)
	assert.Empty(t, missing)
	// The storage sentinel remains distinct from an authentication outcome.
	_, err = userpostgres.New(pool).FindByID(t.Context(), original.ID)
	assert.NotErrorIs(t, err, user.ErrNotFound)
}

func TestLoginRollback(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ name, setup string }{
		{"session insert", `CREATE FUNCTION fail_login() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'test insert failure'; END $$;
   CREATE TRIGGER fail_login BEFORE INSERT ON sessions FOR EACH ROW EXECUTE FUNCTION fail_login()`},
		{"refresh insert", `CREATE FUNCTION fail_login() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'test insert failure'; END $$;
   CREATE TRIGGER fail_login BEFORE INSERT ON refresh_tokens FOR EACH ROW EXECUTE FUNCTION fail_login()`},
		{"commit", `CREATE TABLE commit_probe (value integer UNIQUE DEFERRABLE INITIALLY DEFERRED);
   CREATE FUNCTION fail_login() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN INSERT INTO commit_probe (value) VALUES (1),(1); RETURN NEW; END $$;
   CREATE TRIGGER fail_login AFTER INSERT ON refresh_tokens FOR EACH ROW EXECUTE FUNCTION fail_login()`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			uc, pool, _, _, _ := loginFixture(t)
			_, err := uc.Signup(t.Context(), "rollback@example.com", exactLoginPassword)
			require.NoError(t, err)
			_, err = pool.Exec(t.Context(), tc.setup)
			require.NoError(t, err)
			pair, err := uc.Login(t.Context(), "rollback@example.com", exactLoginPassword)
			assert.Error(t, err)
			assert.NotErrorIs(t, err, auth.ErrAuthenticationFailed)
			assert.Empty(t, pair)
			for _, query := range []string{"SELECT count(*) FROM sessions", "SELECT count(*) FROM refresh_tokens"} {
				var count int
				require.NoError(t, pool.QueryRow(t.Context(), query).Scan(&count))
				assert.Zero(t, count)
			}
		})
	}
}

func TestLoginCancellationRollsBack(t *testing.T) {
	t.Parallel()
	uc, pool, _, _, dependencies := loginFixture(t)
	_, err := uc.Signup(t.Context(), "cancel@example.com", exactLoginPassword)
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	realRepository := sessionpostgres.New(pool)
	repository := authmock.NewSessionRepository(t)
	repository.EXPECT().InsertSession(mock.Anything, mock.Anything).RunAndReturn(realRepository.InsertSession).Once()
	repository.EXPECT().
		InsertRefresh(mock.Anything, mock.Anything).
		RunAndReturn(func(txCtx context.Context, value session.RefreshToken) error {
			if insertErr := realRepository.InsertRefresh(txCtx, value); insertErr != nil {
				return insertErr
			}
			cancel()
			return nil
		}).
		Once()
	dependencies.Sessions = repository
	uc = auth.NewWithLogin(userpostgres.New(pool), password.Argon2id{}, dependencies)
	pair, err := uc.Login(ctx, "cancel@example.com", exactLoginPassword)
	assert.ErrorIs(t, err, context.Canceled)
	assert.Empty(t, pair)
	for _, query := range []string{"SELECT count(*) FROM sessions", "SELECT count(*) FROM refresh_tokens"} {
		var count int
		require.NoError(t, pool.QueryRow(t.Context(), query).Scan(&count))
		assert.Zero(t, count)
	}
}

func TestLoginRepositoryLookupsJoinTransaction(t *testing.T) {
	t.Parallel()
	pool := testutil.Postgres(t)
	repository := userpostgres.New(pool)
	expected := errors.New("rollback fixture")
	var id string
	err := transactionpostgres.New(pool).WithinTransaction(t.Context(), func(ctx context.Context) error {
		created, createErr := repository.Create(ctx, "pending@example.com", "PHC fixture")
		if createErr != nil {
			return createErr
		}
		id = created.ID
		current, lookupErr := repository.FindByID(ctx, created.ID)
		require.NoError(t, lookupErr)
		assert.Equal(t, created, current)
		credentials, hash, lookupErr := repository.FindCredentialsByEmail(ctx, created.Email)
		require.NoError(t, lookupErr)
		assert.Equal(t, created, credentials)
		assert.Equal(t, "PHC fixture", hash)
		return expected
	})
	require.ErrorIs(t, err, expected)
	_, err = repository.FindByID(t.Context(), id)
	assert.ErrorIs(t, err, user.ErrNotFound)
	_, hash, err := repository.FindCredentialsByEmail(t.Context(), "pending@example.com")
	assert.ErrorIs(t, err, user.ErrNotFound)
	assert.Empty(t, hash)
}
