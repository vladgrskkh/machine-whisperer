//go:build integration

package integration_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vladgrskkh/machine-whisperer/api/internal/application"
)

const (
	refreshPath           = "/v1/auth/refresh"
	logoutPath            = "/v1/auth/logout"
	httpCredentials       = `{"email":"sessions@example.com","password":"long enough password"}`
	unknownRefreshToken   = "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	authFailedJSON        = `{"error":{"code":"authentication_failed","message":"Authentication failed"}}`
	internalAuthErrorJSON = `{"error":{"code":"internal_error","message":"An internal error occurred"}}`
)

type httpTokenPair struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	TokenType    string `json:"token_type"`
	ExpiresIn    int    `json:"expires_in"`
}

func TestRefreshAndLogoutHTTP(t *testing.T) {
	t.Parallel()
	pool := accountPostgres.Pool(t)
	server, setElapsed := newSessionServer(t, pool)
	first := startHTTPSession(t, server)
	other := loginHTTPSession(t, server)

	next := readHTTPTokenPair(t, postRefreshToken(t, server, refreshPath, first.RefreshToken))
	assert.NotEqual(t, first.RefreshToken, next.RefreshToken)
	assert.NotEqual(t, first.AccessToken, next.AccessToken)
	sid := httpSessionID(t, first)
	assert.Equal(t, sid, httpSessionID(t, next))

	assertRefreshHTTPRejected(t, postRefreshToken(t, server, refreshPath, unknownRefreshToken))
	assertRefreshHTTPRejected(t, postRefreshToken(t, server, refreshPath, first.RefreshToken))
	assertRefreshHTTPRejected(t, postRefreshToken(t, server, refreshPath, next.RefreshToken))
	state := readRefreshSession(t, pool, sid)
	assert.NotNil(t, state.revokedAt)
	assert.Len(t, state.tokens, 2)

	assertHTTPLogout(t, postRefreshToken(t, server, logoutPath, unknownRefreshToken))
	assertHTTPLogout(t, postRefreshToken(t, server, logoutPath, next.RefreshToken))

	active := readHTTPTokenPair(t, postRefreshToken(t, server, refreshPath, other.RefreshToken))
	assertHTTPLogout(t, postRefreshToken(t, server, logoutPath, other.RefreshToken))
	assertRefreshHTTPRejected(t, postRefreshToken(t, server, refreshPath, active.RefreshToken))
	assert.NotNil(t, readRefreshSession(t, pool, httpSessionID(t, other)).revokedAt)

	expiring := loginHTTPSession(t, server)
	setElapsed(29 * 24 * time.Hour)
	replacement := readHTTPTokenPair(t, postRefreshToken(t, server, refreshPath, expiring.RefreshToken))
	setElapsed(30 * 24 * time.Hour)
	assertRefreshHTTPRejected(t, postRefreshToken(t, server, refreshPath, replacement.RefreshToken))
	assert.Nil(t, readRefreshSession(t, pool, httpSessionID(t, expiring)).revokedAt)
}

func TestRefreshAndLogoutHTTPRejectInvalidBodiesBeforeStorage(t *testing.T) {
	t.Parallel()
	pool := accountPostgres.Pool(t)
	server, _ := newSessionServer(t, pool)
	pair := startHTTPSession(t, server)

	_, err := pool.Exec(t.Context(), "DROP TABLE refresh_tokens")
	require.NoError(t, err)
	valid := refreshBody(pair.RefreshToken)
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
		{"invalid UTF-8", refreshBody(pair.RefreshToken[:42] + "\xff")},
		{"unpaired high surrogate", refreshBody(pair.RefreshToken[:42] + `\ud800`)},
		{"unpaired low surrogate", refreshBody(pair.RefreshToken[:42] + `\udc00`)},
		{"too short", refreshBody(pair.RefreshToken[:42])},
		{"too long", refreshBody(pair.RefreshToken + "A")},
		{"padding", refreshBody(pair.RefreshToken + "=")},
		{"non URL alphabet", refreshBody("+" + pair.RefreshToken[1:])},
		{"noncanonical trailing bits", refreshBody(unknownRefreshToken[:42] + "B")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			for _, path := range []string{refreshPath, logoutPath} {
				response := authHTTPRequest(t, server, http.MethodPost, path, tc.body, nil)
				assert.Equal(t, http.StatusBadRequest, response.status, path)
				assert.JSONEq(
					t,
					`{"error":{"code":"invalid_input","message":"Invalid refresh token or request body"}}`,
					response.body,
				)
				if path == refreshPath {
					assert.Equal(t, "no-store", response.header.Get("Cache-Control"))
				}
			}
		})
	}

	for _, path := range []string{refreshPath, logoutPath} {
		for _, raw := range []string{pair.RefreshToken, unknownRefreshToken} {
			response := postRefreshToken(t, server, path, raw)
			assert.Equal(t, http.StatusInternalServerError, response.status)
			assert.JSONEq(t, internalAuthErrorJSON, response.body)
			if path == refreshPath {
				assert.Equal(t, "no-store", response.header.Get("Cache-Control"))
			}
		}
	}
}

func TestRefreshHTTPMethodErrorsDoNotCache(t *testing.T) {
	t.Parallel()
	pool := accountPostgres.Pool(t)
	server, _ := newSessionServer(t, pool)
	response := authHTTPRequest(t, server, http.MethodGet, refreshPath, "", nil)
	assert.Equal(t, http.StatusMethodNotAllowed, response.status)
	assert.Equal(t, "no-store", response.header.Get("Cache-Control"))
}

type authHTTPResponse struct {
	status int
	header http.Header
	body   string
}

func authHTTPRequest(
	t *testing.T,
	server *httptest.Server,
	method, path, body string,
	headers http.Header,
) authHTTPResponse {
	t.Helper()
	response, err := sendAuthHTTPRequest(t.Context(), server.Client(), method, server.URL+path, body, headers)
	require.NoError(t, err)
	return response
}

func sendAuthHTTPRequest(
	ctx context.Context,
	client *http.Client,
	method, url, body string,
	headers http.Header,
) (authHTTPResponse, error) {
	req, err := http.NewRequestWithContext(ctx, method, url, strings.NewReader(body))
	if err != nil {
		return authHTTPResponse{}, err
	}
	maps.Copy(req.Header, headers)
	if method == http.MethodPost {
		req.Header.Set("Content-Type", "application/json")
	}
	response, err := client.Do(req)
	if err != nil {
		return authHTTPResponse{}, err
	}
	defer response.Body.Close()
	data, err := io.ReadAll(response.Body)
	return authHTTPResponse{status: response.StatusCode, header: response.Header, body: string(data)}, err
}

func refreshBody(secret string) string {
	return `{"refresh_token":"` + secret + `"}`
}

func postRefreshToken(t *testing.T, server *httptest.Server, path, secret string) authHTTPResponse {
	t.Helper()
	return authHTTPRequest(t, server, http.MethodPost, path, refreshBody(secret), nil)
}

func newSessionServer(t *testing.T, pool *pgxpool.Pool) (*httptest.Server, func(time.Duration)) {
	t.Helper()
	var elapsed atomic.Int64
	start := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	server := newAuthServer(t, pool, application.Config{Auth: application.AuthConfig{
		Now: func() time.Time { return start.Add(time.Duration(elapsed.Load())) },
	}})
	t.Cleanup(server.Close)
	return server, func(duration time.Duration) { elapsed.Store(int64(duration)) }
}

func startHTTPSession(t *testing.T, server *httptest.Server) httpTokenPair {
	t.Helper()
	response := authHTTPRequest(t, server, http.MethodPost, "/v1/auth/signup", httpCredentials, nil)
	require.Equal(t, http.StatusCreated, response.status)
	return loginHTTPSession(t, server)
}

func loginHTTPSession(t *testing.T, server *httptest.Server) httpTokenPair {
	t.Helper()
	return readHTTPTokenPair(t, authHTTPRequest(t, server, http.MethodPost, "/v1/auth/login", httpCredentials, nil))
}

func readHTTPTokenPair(t *testing.T, response authHTTPResponse) httpTokenPair {
	t.Helper()
	require.Equal(t, http.StatusOK, response.status)
	assert.Equal(t, "no-store", response.header.Get("Cache-Control"))
	assert.Equal(t, "application/json", response.header.Get("Content-Type"))
	var fields map[string]json.RawMessage
	require.NoError(t, json.Unmarshal([]byte(response.body), &fields))
	assert.Len(t, fields, 4)
	var pair httpTokenPair
	require.NoError(t, json.Unmarshal([]byte(response.body), &pair))
	assert.Equal(t, "Bearer", pair.TokenType)
	assert.Equal(t, 900, pair.ExpiresIn)
	assert.NotEmpty(t, pair.AccessToken)
	decoded, err := base64.RawURLEncoding.Strict().DecodeString(pair.RefreshToken)
	require.NoError(t, err)
	assert.Len(t, decoded, 32)
	assert.Len(t, pair.RefreshToken, 43)
	return pair
}

func httpSessionID(t *testing.T, pair httpTokenPair) string {
	t.Helper()
	claims := jwt.MapClaims{}
	_, _, err := jwt.NewParser().ParseUnverified(pair.AccessToken, claims)
	require.NoError(t, err)
	sid, ok := claims["sid"].(string)
	require.True(t, ok)
	return sid
}

func assertRefreshHTTPRejected(t *testing.T, response authHTTPResponse) {
	t.Helper()
	assert.Equal(t, http.StatusUnauthorized, response.status)
	assert.Equal(t, "no-store", response.header.Get("Cache-Control"))
	assert.JSONEq(t, authFailedJSON, response.body)
}

func assertHTTPLogout(t *testing.T, response authHTTPResponse) {
	t.Helper()
	assert.Equal(t, http.StatusNoContent, response.status)
	assert.Empty(t, response.body)
}
