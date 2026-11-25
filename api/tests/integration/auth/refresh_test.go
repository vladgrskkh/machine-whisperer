//go:build integration

package auth_test

import (
	"crypto/sha256"
	"encoding/base64"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vladgrskkh/machine-whisperer/api/internal/domain/session"
	"github.com/vladgrskkh/machine-whisperer/api/internal/infrastructure/auth/token"
	sessionpostgres "github.com/vladgrskkh/machine-whisperer/api/internal/infrastructure/session/postgres"
	transactionpostgres "github.com/vladgrskkh/machine-whisperer/api/internal/infrastructure/transaction/postgres"
	userpostgres "github.com/vladgrskkh/machine-whisperer/api/internal/infrastructure/user/postgres"
	"github.com/vladgrskkh/machine-whisperer/api/internal/usecase/auth"
)

const refreshTestEmail = "refresh@example.com"

func TestRefreshRotation(t *testing.T) {
	t.Parallel()
	fixture := loginFixture(t)
	original := startRefreshSession(t, fixture)
	originalClaims := verifyRefreshAccess(t, fixture, original)
	now := fixture.now.Add(10 * time.Second)
	refresh := newRefresh(fixture, func() time.Time { return now })
	next, err := refresh.Refresh(t.Context(), original.RefreshToken)
	require.NoError(t, err)
	assert.Equal(t, 900, next.ExpiresIn)
	assert.NotEqual(t, original.RefreshToken, next.RefreshToken)
	claims := verifyRefreshAccess(t, fixture, next)
	assert.Equal(t, originalClaims.UserID, claims.UserID)
	assert.Equal(t, originalClaims.SessionID, claims.SessionID)
	assert.NotEqual(t, originalClaims.JTI, claims.JTI)
	assert.Equal(t, now, claims.IssuedAt)
	assert.Equal(t, now.Add(15*time.Minute), claims.ExpiresAt)
	state := readRefreshSession(t, fixture.pool, claims.SessionID)
	assert.True(t, fixture.now.Add(30*24*time.Hour).Equal(state.expiresAt))
	assert.Nil(t, state.revokedAt)
	require.Len(t, state.tokens, 2)
	assertRefreshConsumedAt(t, state, original.RefreshToken, now)
	successor, exists := state.tokens[refreshHash(t, next.RefreshToken)]
	require.True(t, exists)
	assert.Nil(t, successor)
}

func TestRefreshReuseCommitsRevocation(t *testing.T) {
	t.Parallel()
	fixture := loginFixture(t)
	original := startRefreshSession(t, fixture)
	other := nextLoginSession(t, fixture)
	id := verifyRefreshAccess(t, fixture, original).SessionID
	now := fixture.now.Add(time.Second)
	refresh := newRefresh(fixture, func() time.Time { return now })
	first, err := refresh.Refresh(t.Context(), original.RefreshToken)
	require.NoError(t, err)
	now = now.Add(time.Second)
	second, err := refresh.Refresh(t.Context(), first.RefreshToken)
	require.NoError(t, err)
	state := readRefreshSession(t, fixture.pool, id)
	require.Len(t, state.tokens, 3)
	assertRefreshConsumedAt(t, state, original.RefreshToken, now.Add(-time.Second))
	assertRefreshConsumedAt(t, state, first.RefreshToken, now)

	now = now.Add(time.Second)
	assertRefreshRejected(t, refresh, original.RefreshToken)
	revoked := readRefreshSession(t, fixture.pool, id)
	require.NotNil(t, revoked.revokedAt)
	assert.True(t, revoked.revokedAt.Equal(now))
	assert.Equal(t, state.tokens, revoked.tokens)
	assertRefreshRejected(t, refresh, second.RefreshToken)
	verifyRefreshAccess(t, fixture, second)
	_, err = refresh.Refresh(t.Context(), other.RefreshToken)
	require.NoError(t, err)
}

func TestRefreshAtFixedExpiry(t *testing.T) {
	t.Parallel()
	fixture := loginFixture(t)
	original := startRefreshSession(t, fixture)
	id := verifyRefreshAccess(t, fixture, original).SessionID
	deadline := fixture.now.Add(30 * 24 * time.Hour)
	now := deadline.Add(-2 * time.Second)
	refresh := newRefresh(fixture, func() time.Time { return now })
	first, err := refresh.Refresh(t.Context(), original.RefreshToken)
	require.NoError(t, err)
	now = deadline.Add(-time.Second)
	second, err := refresh.Refresh(t.Context(), first.RefreshToken)
	require.NoError(t, err)
	before := readRefreshSession(t, fixture.pool, id)
	assert.True(t, deadline.Equal(before.expiresAt))
	require.Len(t, before.tokens, 3)

	now = deadline
	assertRefreshRejected(t, refresh, second.RefreshToken)
	assertRefreshRejected(t, refresh, original.RefreshToken)
	assert.Equal(t, before, readRefreshSession(t, fixture.pool, id))
}

func TestRefreshInvalidCredentials(t *testing.T) {
	t.Parallel()
	fixture := loginFixture(t)
	original := startRefreshSession(t, fixture)
	claims := verifyRefreshAccess(t, fixture, original)
	refresh := newRefresh(fixture, func() time.Time { return fixture.now })
	before := readRefreshSession(t, fixture.pool, claims.SessionID)
	assertRefreshRejected(t, refresh, strings.Repeat("A", 43))
	for _, malformed := range []string{
		"", " " + original.RefreshToken, original.RefreshToken + "=", original.RefreshToken + "\n",
		strings.Repeat("A", 42) + "B", strings.Repeat("A", 42), strings.Repeat("A", 42) + "/",
	} {
		pair, err := refresh.Refresh(t.Context(), malformed)
		assert.ErrorIs(t, err, auth.ErrMalformedRefreshToken)
		assert.Empty(t, pair)
	}
	assert.Equal(t, before, readRefreshSession(t, fixture.pool, claims.SessionID))

	_, err := fixture.pool.Exec(t.Context(), "DELETE FROM users WHERE id=$1", claims.UserID)
	require.NoError(t, err)
	assertRefreshRejected(t, refresh, original.RefreshToken)
	assertSessionAbsent(t, fixture.pool, claims.SessionID)
}

func TestLogout(t *testing.T) {
	t.Parallel()
	fixture := loginFixture(t)
	unaffected := startRefreshSession(t, fixture)
	unaffectedID := verifyRefreshAccess(t, fixture, unaffected).SessionID
	unaffectedState := readRefreshSession(t, fixture.pool, unaffectedID)
	now := func() time.Time { return fixture.now }
	logout := newLogout(fixture, now)
	require.NoError(t, logout.Logout(t.Context(), strings.Repeat("A", 43)))
	assert.ErrorIs(t, logout.Logout(t.Context(), unaffected.RefreshToken+"="), auth.ErrMalformedRefreshToken)
	assert.Equal(t, unaffectedState, readRefreshSession(t, fixture.pool, unaffectedID))

	for _, tc := range []struct {
		name    string
		advance time.Duration
		rotate  bool
	}{
		{name: "current", advance: time.Second},
		{name: "consumed", advance: time.Second, rotate: true},
		{name: "expired", advance: 30 * 24 * time.Hour},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			original := nextLoginSession(t, fixture)
			id := verifyRefreshAccess(t, fixture, original).SessionID
			now := fixture.now.Add(tc.advance)
			clock := func() time.Time { return now }
			refresh := newRefresh(fixture, clock)
			current := original
			if tc.rotate {
				var err error
				current, err = refresh.Refresh(t.Context(), original.RefreshToken)
				require.NoError(t, err)
			}
			before := readRefreshSession(t, fixture.pool, id)
			logout := newLogout(fixture, clock)
			require.NoError(t, logout.Logout(t.Context(), original.RefreshToken))
			revoked := readRefreshSession(t, fixture.pool, id)
			require.NotNil(t, revoked.revokedAt)
			assert.True(t, revoked.revokedAt.Equal(now))
			assert.Equal(t, before.tokens, revoked.tokens)
			assert.True(t, before.expiresAt.Equal(revoked.expiresAt))
			assertRefreshRejected(t, refresh, current.RefreshToken)

			now = now.Add(time.Second)
			require.NoError(t, logout.Logout(t.Context(), current.RefreshToken))
			assert.Equal(t, revoked, readRefreshSession(t, fixture.pool, id))
			assert.Equal(t, unaffectedState, readRefreshSession(t, fixture.pool, unaffectedID))
		})
	}
}

func assertRefreshRejected(t *testing.T, refresh *auth.Refresh, raw string) {
	t.Helper()
	pair, err := refresh.Refresh(t.Context(), raw)
	assert.ErrorIs(t, err, auth.ErrAuthenticationFailed)
	assert.Empty(t, pair)
}

func startRefreshSession(t *testing.T, fixture loginFixtures) auth.TokenPair {
	t.Helper()
	_, err := fixture.signup.Signup(t.Context(), refreshTestEmail, exactLoginPassword)
	require.NoError(t, err)
	return nextLoginSession(t, fixture)
}

func nextLoginSession(t *testing.T, fixture loginFixtures) auth.TokenPair {
	t.Helper()
	pair, err := fixture.login.Login(t.Context(), refreshTestEmail, exactLoginPassword)
	require.NoError(t, err)
	return pair
}

func newRefresh(fixture loginFixtures, now func() time.Time) *auth.Refresh {
	return auth.NewRefresh(
		sessionpostgres.New(fixture.pool), userpostgres.New(fixture.pool), transactionpostgres.New(fixture.pool),
		fixture.signer, token.Refresh{}, token.IDs{}, now,
	)
}

func newLogout(fixture loginFixtures, now func() time.Time) *auth.Logout {
	return auth.NewLogout(sessionpostgres.New(fixture.pool), transactionpostgres.New(fixture.pool), now)
}

func verifyRefreshAccess(t *testing.T, fixture loginFixtures, pair auth.TokenPair) session.AccessClaims {
	t.Helper()
	claims, err := fixture.signer.Verify(t.Context(), pair.AccessToken)
	require.NoError(t, err)
	return claims
}

func refreshHash(t *testing.T, raw string) [32]byte {
	t.Helper()
	secret, err := base64.RawURLEncoding.Strict().DecodeString(raw)
	require.NoError(t, err)
	require.Len(t, secret, 32)
	return sha256.Sum256(secret)
}

type refreshSessionState struct {
	expiresAt time.Time
	revokedAt *time.Time
	tokens    map[[32]byte]*time.Time
}

func readRefreshSession(t *testing.T, pool *pgxpool.Pool, id string) refreshSessionState {
	t.Helper()
	state := refreshSessionState{tokens: make(map[[32]byte]*time.Time)}
	require.NoError(
		t,
		pool.QueryRow(t.Context(), "SELECT expires_at,revoked_at FROM sessions WHERE id=$1", id).
			Scan(&state.expiresAt, &state.revokedAt),
	)
	rows, err := pool.Query(t.Context(), "SELECT hash,consumed_at FROM refresh_tokens WHERE session_id=$1", id)
	require.NoError(t, err)
	defer rows.Close()
	for rows.Next() {
		var hash []byte
		var consumedAt *time.Time
		require.NoError(t, rows.Scan(&hash, &consumedAt))
		require.Len(t, hash, 32)
		state.tokens[[32]byte(hash)] = consumedAt
	}
	require.NoError(t, rows.Err())
	return state
}

func assertRefreshConsumedAt(t *testing.T, state refreshSessionState, raw string, at time.Time) {
	t.Helper()
	consumed := state.tokens[refreshHash(t, raw)]
	require.NotNil(t, consumed)
	assert.True(t, consumed.Equal(at))
}
