//go:build integration

package integration_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgerrcode"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/vladgrskkh/machine-whisperer/api/internal/infrastructure/auth/token"
	sessionpostgres "github.com/vladgrskkh/machine-whisperer/api/internal/infrastructure/session/postgres"
	transactionpostgres "github.com/vladgrskkh/machine-whisperer/api/internal/infrastructure/transaction/postgres"
	"github.com/vladgrskkh/machine-whisperer/api/internal/usecase/auth"
	"github.com/vladgrskkh/machine-whisperer/api/internal/usecase/auth/authmock"
)

//nolint:tparallel // Each failure must leave the same predecessor usable for the next attempt.
func TestRefreshFailuresRollBack(t *testing.T) {
	t.Parallel()
	fixture := loginFixture(t)
	original := startRefreshSession(t, fixture)
	id := verifyRefreshAccess(t, fixture, original).SessionID
	before := readRefreshSession(t, fixture.pool, id)
	now := func() time.Time { return fixture.now.Add(time.Second) }
	repository := sessionpostgres.New(fixture.pool)
	manager := transactionpostgres.New(fixture.pool)
	expected := errors.New("injected refresh failure")

	t.Run("signer", func(t *testing.T) {
		signer := authmock.NewAccessSigner(t)
		signer.EXPECT().Sign(mock.Anything, mock.Anything).Return("", expected).Once()
		refresh := auth.NewRefresh(repository, manager, signer, token.Refresh{}, token.IDs{}, now)
		pair, err := refresh.Refresh(t.Context(), original.RefreshToken)
		assert.ErrorIs(t, err, expected)
		assert.Empty(t, pair)
		assert.Equal(t, before, readRefreshSession(t, fixture.pool, id))
	})
	t.Run("entropy", func(t *testing.T) {
		entropy := authmock.NewRefreshGenerator(t)
		entropy.EXPECT().Generate(mock.Anything).Return("", [32]byte{}, expected).Once()
		refresh := auth.NewRefresh(repository, manager, fixture.signer, entropy, token.IDs{}, now)
		pair, err := refresh.Refresh(t.Context(), original.RefreshToken)
		assert.ErrorIs(t, err, expected)
		assert.Empty(t, pair)
		assert.Equal(t, before, readRefreshSession(t, fixture.pool, id))
	})
	t.Run("ID generation", func(t *testing.T) {
		ids := authmock.NewIDGenerator(t)
		ids.EXPECT().Generate(mock.Anything).Return("", expected).Once()
		refresh := auth.NewRefresh(repository, manager, fixture.signer, token.Refresh{}, ids, now)
		pair, err := refresh.Refresh(t.Context(), original.RefreshToken)
		assert.ErrorIs(t, err, expected)
		assert.Empty(t, pair)
		assert.Equal(t, before, readRefreshSession(t, fixture.pool, id))
	})
	t.Run("successor insert", func(t *testing.T) {
		entropy := authmock.NewRefreshGenerator(t)
		entropy.EXPECT().Generate(mock.Anything).
			Return(original.RefreshToken, refreshHash(t, original.RefreshToken), nil).Once()
		refresh := auth.NewRefresh(repository, manager, fixture.signer, entropy, token.IDs{}, now)
		pair, err := refresh.Refresh(t.Context(), original.RefreshToken)
		requirePGCode(t, err, pgerrcode.UniqueViolation)
		assert.Empty(t, pair)
		assert.Equal(t, before, readRefreshSession(t, fixture.pool, id))
	})
	t.Run("cancellation before commit", func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		transactions := authmock.NewTransactionManager(t)
		transactions.EXPECT().WithinTransaction(mock.Anything, mock.Anything).
			RunAndReturn(func(ctx context.Context, run func(context.Context) error) error {
				return manager.WithinTransaction(ctx, func(txCtx context.Context) error {
					err := run(txCtx)
					cancel()
					return err
				})
			}).Once()
		refresh := auth.NewRefresh(repository, transactions, fixture.signer, token.Refresh{}, token.IDs{}, now)
		pair, err := refresh.Refresh(ctx, original.RefreshToken)
		assert.ErrorIs(t, err, context.Canceled)
		assert.Empty(t, pair)
		assert.Equal(t, before, readRefreshSession(t, fixture.pool, id))
	})
	t.Run("commit failure", func(t *testing.T) {
		_, err := fixture.pool.Exec(t.Context(), `
			CREATE TABLE commit_probe (value integer UNIQUE DEFERRABLE INITIALLY DEFERRED);
			CREATE FUNCTION fail_refresh_commit() RETURNS trigger LANGUAGE plpgsql AS $$
			BEGIN INSERT INTO commit_probe (value) VALUES (1),(1); RETURN NEW; END $$;
			CREATE TRIGGER fail_refresh_commit AFTER INSERT ON refresh_tokens
			FOR EACH ROW EXECUTE FUNCTION fail_refresh_commit()`)
		require.NoError(t, err)
		pair, err := newRefresh(fixture, now).Refresh(t.Context(), original.RefreshToken)
		requirePGCode(t, err, pgerrcode.UniqueViolation)
		assert.Empty(t, pair)
		assert.Equal(t, before, readRefreshSession(t, fixture.pool, id))
	})
}

func TestRefreshAndLogoutRevocationFailure(t *testing.T) {
	t.Parallel()
	fixture := loginFixture(t)
	original := startRefreshSession(t, fixture)
	id := verifyRefreshAccess(t, fixture, original).SessionID
	now := func() time.Time { return fixture.now.Add(time.Second) }
	refresh, logout := newRefresh(fixture, now), newLogout(fixture, now)
	current, err := refresh.Refresh(t.Context(), original.RefreshToken)
	require.NoError(t, err)
	before := readRefreshSession(t, fixture.pool, id)
	_, err = fixture.pool.Exec(t.Context(), `
		CREATE FUNCTION fail_session_revoke() RETURNS trigger LANGUAGE plpgsql AS $$
		BEGIN RAISE check_violation; END $$;
		CREATE TRIGGER fail_session_revoke BEFORE UPDATE OF revoked_at ON sessions
		FOR EACH ROW EXECUTE FUNCTION fail_session_revoke()`)
	require.NoError(t, err)

	pair, err := refresh.Refresh(t.Context(), original.RefreshToken)
	requirePGCode(t, err, pgerrcode.CheckViolation)
	assert.NotErrorIs(t, err, auth.ErrAuthenticationFailed)
	assert.Empty(t, pair)
	requirePGCode(t, logout.Logout(t.Context(), current.RefreshToken), pgerrcode.CheckViolation)
	assert.Equal(t, before, readRefreshSession(t, fixture.pool, id))
}

func TestRefreshAndLogoutLookupFailure(t *testing.T) {
	t.Parallel()
	fixture := loginFixture(t)
	original := startRefreshSession(t, fixture)
	now := func() time.Time { return fixture.now }
	_, err := fixture.pool.Exec(t.Context(), "ALTER TABLE refresh_tokens RENAME TO refresh_tokens_unavailable")
	require.NoError(t, err)

	pair, err := newRefresh(fixture, now).Refresh(t.Context(), original.RefreshToken)
	requirePGCode(t, err, pgerrcode.UndefinedTable)
	assert.Empty(t, pair)
	requirePGCode(t, newLogout(fixture, now).Logout(t.Context(), original.RefreshToken), pgerrcode.UndefinedTable)
}
