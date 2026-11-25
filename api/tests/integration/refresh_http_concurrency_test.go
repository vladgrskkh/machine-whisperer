//go:build integration

package integration_test

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
)

func TestRefreshHTTPSameTokenRaceRevokesTheOnlySuccessor(t *testing.T) {
	t.Parallel()
	pool := accountPostgres.Pool(t)
	server, race := newAuthRaceServer(t, pool)
	initial := startHTTPSession(t, server)
	independent := loginHTTPSession(t, server)

	responses := race(refreshPath, refreshPath, initial.RefreshToken)
	if responses[0].status == http.StatusUnauthorized {
		responses[0], responses[1] = responses[1], responses[0]
	}
	next := readHTTPTokenPair(t, responses[0])
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
	assertRefreshHTTPRejected(t, postRefreshToken(t, server, refreshPath, next.RefreshToken))
	readHTTPTokenPair(t, postRefreshToken(t, server, refreshPath, independent.RefreshToken))
}

func TestRefreshHTTPLogoutRaceAlwaysRevokesSession(t *testing.T) {
	t.Parallel()
	pool := accountPostgres.Pool(t)
	server, race := newAuthRaceServer(t, pool)
	initial := startHTTPSession(t, server)
	independent := loginHTTPSession(t, server)

	responses := race(refreshPath, logoutPath, initial.RefreshToken)
	assertHTTPLogout(t, responses[1])
	sid := httpSessionID(t, initial)
	state := readRefreshSession(t, pool, sid)
	assert.NotNil(t, state.revokedAt)
	oldHash := refreshHash(t, initial.RefreshToken)
	if responses[0].status == http.StatusOK {
		next := readHTTPTokenPair(t, responses[0])
		assert.Equal(t, sid, httpSessionID(t, next))
		assert.Len(t, state.tokens, 2)
		assert.NotNil(t, state.tokens[oldHash])
		newHash := refreshHash(t, next.RefreshToken)
		assert.Contains(t, state.tokens, newHash)
		assert.Nil(t, state.tokens[newHash])
		assertRefreshHTTPRejected(t, postRefreshToken(t, server, refreshPath, next.RefreshToken))
	} else {
		assertRefreshHTTPRejected(t, responses[0])
		assert.Len(t, state.tokens, 1)
		assert.Contains(t, state.tokens, oldHash)
		assert.Nil(t, state.tokens[oldHash])
	}
	assertRefreshHTTPRejected(t, postRefreshToken(t, server, refreshPath, initial.RefreshToken))
	readHTTPTokenPair(t, postRefreshToken(t, server, refreshPath, independent.RefreshToken))
}

func newAuthRaceServer(
	t *testing.T,
	pool *pgxpool.Pool,
) (*httptest.Server, func(firstPath, secondPath, secret string) [2]authHTTPResponse) {
	t.Helper()
	app, err := application.New(t.Context(), authConfig(t), application.WithPool(pool))
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

	return server, func(firstPath, secondPath, secret string) [2]authHTTPResponse {
		t.Helper()
		ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
		defer cancel()

		type result struct {
			response authHTTPResponse
			err      error
		}
		var results [2]chan result
		for i, path := range []string{firstPath, secondPath} {
			client := &http.Client{Transport: &http.Transport{}}
			defer client.CloseIdleConnections()
			channel := make(chan result, 1)
			results[i] = channel
			go func() {
				response, sendErr := sendAuthHTTPRequest(
					ctx,
					client,
					http.MethodPost,
					server.URL+path,
					refreshBody(secret),
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
		var responses [2]authHTTPResponse
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
