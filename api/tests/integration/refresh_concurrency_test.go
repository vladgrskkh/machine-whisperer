//go:build integration

package integration_test

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/vladgrskkh/machine-whisperer/api/internal/domain/session"
	"github.com/vladgrskkh/machine-whisperer/api/internal/infrastructure/auth/token"
	sessionpostgres "github.com/vladgrskkh/machine-whisperer/api/internal/infrastructure/session/postgres"
	transactionpostgres "github.com/vladgrskkh/machine-whisperer/api/internal/infrastructure/transaction/postgres"
	"github.com/vladgrskkh/machine-whisperer/api/internal/usecase/auth"
	"github.com/vladgrskkh/machine-whisperer/api/internal/usecase/auth/authmock"
)

const authRaceTimeout = 10 * time.Second

func TestRefreshConcurrentSameToken(t *testing.T) {
	t.Parallel()
	fixture := loginFixture(t)
	original := startRefreshSession(t, fixture)
	id := verifyRefreshAccess(t, fixture, original).SessionID
	now := fixture.now.Add(time.Second)
	at := func() time.Time { return now }
	refresh := refreshOperation(fixture, fixture.signer, at, original.RefreshToken)
	results := raceAuthOperations(t, fixture.pool, refresh, refresh)
	var winner auth.TokenPair
	var successes int
	for _, result := range results {
		if result.err == nil {
			winner = result.pair
			successes++
			continue
		}
		assert.ErrorIs(t, result.err, auth.ErrAuthenticationFailed)
		assert.Empty(t, result.pair)
	}
	require.Equal(t, 1, successes)

	state := readRefreshSession(t, fixture.pool, id)
	require.NotNil(t, state.revokedAt)
	assert.True(t, state.revokedAt.Equal(now))
	require.Len(t, state.tokens, 2)
	assertRefreshConsumedAt(t, state, original.RefreshToken, now)
	consumed, exists := state.tokens[refreshHash(t, winner.RefreshToken)]
	require.True(t, exists)
	assert.Nil(t, consumed)
	assertRefreshRejected(t, newRefresh(fixture, at), winner.RefreshToken)
	assert.Equal(t, id, verifyRefreshAccess(t, fixture, winner).SessionID)
}

func TestRefreshConcurrentIndependentSessions(t *testing.T) {
	t.Parallel()
	fixture := loginFixture(t)
	first := startRefreshSession(t, fixture)
	second := nextLoginSession(t, fixture)
	at := func() time.Time { return fixture.now.Add(time.Second) }

	// Both transactions must reach signing while holding their respective session locks.
	var ready atomic.Int32
	release := make(chan struct{})
	signer := authmock.NewAccessSigner(t)
	signer.EXPECT().Sign(mock.Anything, mock.Anything).
		RunAndReturn(func(ctx context.Context, claims session.AccessClaims) (string, error) {
			if ready.Add(1) == 2 {
				close(release)
			}
			select {
			case <-release:
				return fixture.signer.Sign(ctx, claims)
			case <-ctx.Done():
				return "", ctx.Err()
			}
		}).Twice()
	results := raceAuthOperations(t, fixture.pool,
		refreshOperation(fixture, signer, at, first.RefreshToken),
		refreshOperation(fixture, signer, at, second.RefreshToken),
	)
	for _, result := range results {
		require.NoError(t, result.err)
		id := verifyRefreshAccess(t, fixture, result.pair).SessionID
		state := readRefreshSession(t, fixture.pool, id)
		assert.Nil(t, state.revokedAt)
		assert.Len(t, state.tokens, 2)
	}
}

func TestRefreshConcurrentLogoutAndReuse(t *testing.T) {
	t.Parallel()
	fixture := loginFixture(t)
	first := startRefreshSession(t, fixture)
	at := func() time.Time { return fixture.now.Add(time.Second) }
	for _, name := range []string{"logout", "older token reuse"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			original := first
			competing := func(ctx context.Context, transactions auth.TransactionManager) (auth.TokenPair, error) {
				logout := auth.NewLogout(sessionpostgres.New(fixture.pool), transactions, at)
				return auth.TokenPair{}, logout.Logout(ctx, original.RefreshToken)
			}
			current := original
			if name == "older token reuse" {
				original = nextLoginSession(t, fixture)
				var err error
				current, err = newRefresh(fixture, at).Refresh(t.Context(), original.RefreshToken)
				require.NoError(t, err)
				competing = refreshOperation(fixture, fixture.signer, at, original.RefreshToken)
			}
			id := verifyRefreshAccess(t, fixture, current).SessionID
			before := readRefreshSession(t, fixture.pool, id)
			results := raceAuthOperations(
				t,
				fixture.pool,
				refreshOperation(fixture, fixture.signer, at, current.RefreshToken),
				competing,
			)
			rotated, revoked := results[0], results[1]
			if name == "logout" {
				require.NoError(t, revoked.err)
			} else {
				require.ErrorIs(t, revoked.err, auth.ErrAuthenticationFailed)
			}
			assert.Empty(t, revoked.pair)
			state := readRefreshSession(t, fixture.pool, id)
			require.NotNil(t, state.revokedAt)
			assert.True(t, state.revokedAt.Equal(at()))
			if rotated.err == nil {
				assert.Len(t, state.tokens, len(before.tokens)+1)
				assertRefreshConsumedAt(t, state, current.RefreshToken, at())
				assertRefreshRejected(t, newRefresh(fixture, at), rotated.pair.RefreshToken)
			} else {
				assert.ErrorIs(t, rotated.err, auth.ErrAuthenticationFailed)
				assert.Empty(t, rotated.pair)
				assert.Equal(t, before.tokens, state.tokens)
			}
			assertRefreshRejected(t, newRefresh(fixture, at), current.RefreshToken)
		})
	}
}

type authRaceOperation func(context.Context, auth.TransactionManager) (auth.TokenPair, error)

type authRaceResult struct {
	pair auth.TokenPair
	err  error
}

func refreshOperation(
	fixture loginFixtures,
	signer auth.AccessSigner,
	now func() time.Time,
	raw string,
) authRaceOperation {
	return func(ctx context.Context, transactions auth.TransactionManager) (auth.TokenPair, error) {
		refresh := auth.NewRefresh(
			sessionpostgres.New(fixture.pool),
			transactions,
			signer,
			token.Refresh{},
			token.IDs{},
			now,
		)
		return refresh.Refresh(ctx, raw)
	}
}

func raceAuthOperations(t *testing.T, pool *pgxpool.Pool, first, second authRaceOperation) [2]authRaceResult {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), authRaceTimeout)
	defer cancel()

	manager := transactionpostgres.New(pool)
	transactions := authmock.NewTransactionManager(t)
	ready := make(chan struct{}, 2)
	release := make(chan struct{})
	transactions.EXPECT().WithinTransaction(mock.Anything, mock.Anything).
		RunAndReturn(func(ctx context.Context, run func(context.Context) error) error {
			return manager.WithinTransaction(ctx, func(txCtx context.Context) error {
				ready <- struct{}{}
				select {
				case <-release:
					return run(txCtx)
				case <-txCtx.Done():
					return txCtx.Err()
				}
			})
		}).Twice()

	var results [2]chan authRaceResult
	for i, operation := range []authRaceOperation{first, second} {
		results[i] = make(chan authRaceResult, 1)
		go func() {
			pair, err := operation(ctx, transactions)
			results[i] <- authRaceResult{pair: pair, err: err}
		}()
	}
	for range results {
		select {
		case <-ready:
		case <-ctx.Done():
			t.Fatal("waiting for separate auth transactions:", ctx.Err())
		}
	}
	close(release)

	var out [2]authRaceResult
	for i, result := range results {
		select {
		case out[i] = <-result:
		case <-ctx.Done():
			t.Fatal("waiting for auth transaction to finish:", ctx.Err())
		}
	}
	return out
}
