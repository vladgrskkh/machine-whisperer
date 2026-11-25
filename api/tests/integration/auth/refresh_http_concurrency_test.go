//go:build integration

package auth_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vladgrskkh/machine-whisperer/api/internal/application"
	"github.com/vladgrskkh/machine-whisperer/api/internal/testutil"
	"github.com/vladgrskkh/machine-whisperer/api/tests/integration/internal/testfixture"
)

func TestRefreshHTTPSameTokenRaceRevokesTheOnlySuccessor(t *testing.T) {
	t.Parallel()
	pool := testutil.Postgres(t)
	server, race := newAuthRaceServer(t, pool)
	initial := testfixture.StartHTTPSession(t, server)
	independent := testfixture.LoginHTTPSession(t, server)

	responses := race(testfixture.RefreshPath, testfixture.RefreshPath, initial.RefreshToken)
	if responses[0].Status == http.StatusUnauthorized {
		responses[0], responses[1] = responses[1], responses[0]
	}
	next := testfixture.ReadHTTPTokenPair(t, responses[0])
	assertRefreshHTTPRejected(t, responses[1])
	sid := httpSessionID(t, initial)
	assert.Equal(t, sid, httpSessionID(t, next))
	state := readRefreshSession(t, pool, sid)
	assert.NotNil(t, state.revokedAt)
	assert.Len(t, state.tokens, 2)
	assert.NotNil(t, state.tokens[refreshHash(t, initial.RefreshToken)])
	hash := refreshHash(t, next.RefreshToken)
	assert.Contains(t, state.tokens, hash)
	assert.Nil(t, state.tokens[hash])
	assertRefreshHTTPRejected(t, testfixture.PostRefreshToken(t, server, testfixture.RefreshPath, next.RefreshToken))
	testfixture.ReadHTTPTokenPair(
		t,
		testfixture.PostRefreshToken(t, server, testfixture.RefreshPath, independent.RefreshToken),
	)
}

func TestRefreshHTTPLogoutRaceAlwaysRevokesSession(t *testing.T) {
	t.Parallel()
	pool := testutil.Postgres(t)
	server, race := newAuthRaceServer(t, pool)
	initial := testfixture.StartHTTPSession(t, server)
	independent := testfixture.LoginHTTPSession(t, server)

	responses := race(testfixture.RefreshPath, logoutPath, initial.RefreshToken)
	assertHTTPLogout(t, responses[1])
	sid := httpSessionID(t, initial)
	state := readRefreshSession(t, pool, sid)
	assert.NotNil(t, state.revokedAt)
	oldHash := refreshHash(t, initial.RefreshToken)
	if responses[0].Status == http.StatusOK {
		next := testfixture.ReadHTTPTokenPair(t, responses[0])
		assert.Equal(t, sid, httpSessionID(t, next))
		assert.Len(t, state.tokens, 2)
		assert.NotNil(t, state.tokens[oldHash])
		newHash := refreshHash(t, next.RefreshToken)
		assert.Contains(t, state.tokens, newHash)
		assert.Nil(t, state.tokens[newHash])
		assertRefreshHTTPRejected(
			t,
			testfixture.PostRefreshToken(t, server, testfixture.RefreshPath, next.RefreshToken),
		)
	} else {
		assertRefreshHTTPRejected(t, responses[0])
		assert.Len(t, state.tokens, 1)
		assert.Contains(t, state.tokens, oldHash)
		assert.Nil(t, state.tokens[oldHash])
	}
	assertRefreshHTTPRejected(t, testfixture.PostRefreshToken(t, server, testfixture.RefreshPath, initial.RefreshToken))
	testfixture.ReadHTTPTokenPair(
		t,
		testfixture.PostRefreshToken(t, server, testfixture.RefreshPath, independent.RefreshToken),
	)
}

func newAuthRaceServer(
	t *testing.T,
	pool *pgxpool.Pool,
) (*httptest.Server, func(firstPath, secondPath, secret string) [2]testfixture.AuthHTTPResponse) {
	t.Helper()
	app, err := application.New(t.Context(), testfixture.AuthConfig(t), application.WithPool(pool))
	require.NoError(t, err)
	t.Cleanup(app.Close)

	const raceHeader = "X-Test-Auth-Race"
	ready := make(chan struct{}, 2)
	release := make(chan struct{})
	routes := app.Routes()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get(raceHeader) == "1" {
			ready <- struct{}{}
			select {
			case <-release:
			case <-r.Context().Done():
				return
			}
		}
		routes.ServeHTTP(w, r)
	}))
	t.Cleanup(server.Close)

	return server, func(firstPath, secondPath, secret string) [2]testfixture.AuthHTTPResponse {
		t.Helper()
		ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
		defer cancel()

		type result struct {
			response testfixture.AuthHTTPResponse
			err      error
		}
		var results [2]chan result
		for i, path := range []string{firstPath, secondPath} {
			client := &http.Client{Transport: &http.Transport{}}
			defer client.CloseIdleConnections()
			channel := make(chan result, 1)
			results[i] = channel
			go func() {
				response, sendErr := testfixture.SendAuthHTTPRequest(
					ctx,
					client,
					http.MethodPost,
					server.URL+path,
					testfixture.RefreshBody(secret),
					http.Header{raceHeader: {"1"}},
				)
				channel <- result{response: response, err: sendErr}
			}()
		}
		for range results {
			select {
			case <-ready:
			case <-ctx.Done():
				t.Fatal("waiting for both HTTP requests:", ctx.Err())
			}
		}
		close(release)
		var responses [2]testfixture.AuthHTTPResponse
		for i, channel := range results {
			select {
			case result := <-channel:
				require.NoError(t, result.err)
				responses[i] = result.response
			case <-ctx.Done():
				t.Fatal("waiting for both HTTP responses:", ctx.Err())
			}
		}
		return responses
	}
}
