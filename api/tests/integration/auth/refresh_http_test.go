//go:build integration

package auth_test

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vladgrskkh/machine-whisperer/api/internal/testutil"
	"github.com/vladgrskkh/machine-whisperer/api/tests/integration/internal/testfixture"
)

const logoutPath = "/v1/auth/logout"

func TestRefreshAndLogoutHTTP(t *testing.T) {
	t.Parallel()
	pool := testutil.Postgres(t)
	server, setElapsed := testfixture.NewSessionServer(t, pool)
	first := testfixture.StartHTTPSession(t, server)
	other := testfixture.LoginHTTPSession(t, server)

	next := testfixture.ReadHTTPTokenPair(
		t,
		testfixture.PostRefreshToken(t, server, testfixture.RefreshPath, first.RefreshToken),
	)
	assert.NotEqual(t, first.RefreshToken, next.RefreshToken)
	assert.NotEqual(t, first.AccessToken, next.AccessToken)
	sid := httpSessionID(t, first)
	assert.Equal(t, sid, httpSessionID(t, next))

	assertRefreshHTTPRejected(
		t,
		testfixture.PostRefreshToken(t, server, testfixture.RefreshPath, testfixture.UnknownRefreshToken),
	)
	assertRefreshHTTPRejected(t, testfixture.PostRefreshToken(t, server, testfixture.RefreshPath, first.RefreshToken))
	assertRefreshHTTPRejected(t, testfixture.PostRefreshToken(t, server, testfixture.RefreshPath, next.RefreshToken))
	state := readRefreshSession(t, pool, sid)
	assert.NotNil(t, state.revokedAt)
	assert.Len(t, state.tokens, 2)

	assertHTTPLogout(t, testfixture.PostRefreshToken(t, server, logoutPath, testfixture.UnknownRefreshToken))
	assertHTTPLogout(t, testfixture.PostRefreshToken(t, server, logoutPath, next.RefreshToken))

	active := testfixture.ReadHTTPTokenPair(
		t,
		testfixture.PostRefreshToken(t, server, testfixture.RefreshPath, other.RefreshToken),
	)
	assertHTTPLogout(t, testfixture.PostRefreshToken(t, server, logoutPath, other.RefreshToken))
	assertRefreshHTTPRejected(t, testfixture.PostRefreshToken(t, server, testfixture.RefreshPath, active.RefreshToken))
	assert.NotNil(t, readRefreshSession(t, pool, httpSessionID(t, other)).revokedAt)

	expiring := testfixture.LoginHTTPSession(t, server)
	setElapsed(29 * 24 * time.Hour)
	replacement := testfixture.ReadHTTPTokenPair(
		t,
		testfixture.PostRefreshToken(t, server, testfixture.RefreshPath, expiring.RefreshToken),
	)
	setElapsed(30 * 24 * time.Hour)
	assertRefreshHTTPRejected(
		t,
		testfixture.PostRefreshToken(t, server, testfixture.RefreshPath, replacement.RefreshToken),
	)
	assert.Nil(t, readRefreshSession(t, pool, httpSessionID(t, expiring)).revokedAt)
}

func TestRefreshAndLogoutHTTPRejectInvalidBodiesBeforeStorage(t *testing.T) {
	t.Parallel()
	pool := testutil.Postgres(t)
	server, _ := testfixture.NewSessionServer(t, pool)
	pair := testfixture.StartHTTPSession(t, server)

	_, err := pool.Exec(t.Context(), "DROP TABLE refresh_tokens")
	require.NoError(t, err)
	valid := testfixture.RefreshBody(pair.RefreshToken)
	for _, tc := range []struct {
		name string
		body string
	}{
		{"malformed JSON", "{"},
		{"null body", "null"},
		{"array", "[]"},
		{"missing field", "{}"},
		{"null token", `{"refresh_token":null}`},
		{"numeric token", `{"refresh_token":1}`},
		{"object token", `{"refresh_token":{}}`},
		{"unknown field", `{"extra":true,` + valid[1:]},
		{"trailing JSON", valid + " {}"},
		{"oversized", valid + strings.Repeat(" ", 8192)},
		{"invalid UTF-8", testfixture.RefreshBody(pair.RefreshToken[:42] + "\xff")},
		{"unpaired high surrogate", testfixture.RefreshBody(pair.RefreshToken[:42] + `\ud800`)},
		{"unpaired low surrogate", testfixture.RefreshBody(pair.RefreshToken[:42] + `\udc00`)},
		{"too short", testfixture.RefreshBody(pair.RefreshToken[:42])},
		{"too long", testfixture.RefreshBody(pair.RefreshToken + "A")},
		{"padding", testfixture.RefreshBody(pair.RefreshToken + "=")},
		{"non URL alphabet", testfixture.RefreshBody("+" + pair.RefreshToken[1:])},
		{"noncanonical trailing bits", testfixture.RefreshBody(testfixture.UnknownRefreshToken[:42] + "B")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			for _, path := range []string{testfixture.RefreshPath, logoutPath} {
				response := testfixture.AuthHTTPRequest(t, server, http.MethodPost, path, tc.body, nil)
				assert.Equal(t, http.StatusBadRequest, response.Status, path)
				assert.JSONEq(
					t,
					`{"error":{"code":"invalid_input","message":"Invalid refresh token or request body"}}`,
					response.Body,
				)
				if path == testfixture.RefreshPath {
					assert.Equal(t, "no-store", response.Header.Get("Cache-Control"))
				}
			}
		})
	}

	for _, path := range []string{testfixture.RefreshPath, logoutPath} {
		for _, raw := range []string{pair.RefreshToken, testfixture.UnknownRefreshToken} {
			response := testfixture.PostRefreshToken(t, server, path, raw)
			assert.Equal(t, http.StatusInternalServerError, response.Status)
			assert.JSONEq(t, testfixture.InternalAuthErrorJSON, response.Body)
			if path == testfixture.RefreshPath {
				assert.Equal(t, "no-store", response.Header.Get("Cache-Control"))
			}
		}
	}
}

func TestRefreshHTTPMethodErrorsDoNotCache(t *testing.T) {
	t.Parallel()
	pool := testutil.Postgres(t)
	server, _ := testfixture.NewSessionServer(t, pool)
	response := testfixture.AuthHTTPRequest(t, server, http.MethodGet, testfixture.RefreshPath, "", nil)
	assert.Equal(t, http.StatusMethodNotAllowed, response.Status)
	assert.Equal(t, "no-store", response.Header.Get("Cache-Control"))
}

func httpSessionID(t *testing.T, pair testfixture.HTTPTokenPair) string {
	t.Helper()
	claims := jwt.MapClaims{}
	_, _, err := jwt.NewParser().ParseUnverified(pair.AccessToken, claims)
	require.NoError(t, err)
	sid, ok := claims["sid"].(string)
	require.True(t, ok)
	return sid
}

func assertRefreshHTTPRejected(t *testing.T, response testfixture.AuthHTTPResponse) {
	t.Helper()
	assert.Equal(t, http.StatusUnauthorized, response.Status)
	assert.Equal(t, "no-store", response.Header.Get("Cache-Control"))
	assert.JSONEq(t, testfixture.AuthFailedJSON, response.Body)
}

func assertHTTPLogout(t *testing.T, response testfixture.AuthHTTPResponse) {
	t.Helper()
	assert.Equal(t, http.StatusNoContent, response.Status)
	assert.Empty(t, response.Body)
}
