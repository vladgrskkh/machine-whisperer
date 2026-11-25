//go:build integration

package testfixture

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

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vladgrskkh/machine-whisperer/api/internal/application"
	"github.com/vladgrskkh/machine-whisperer/api/internal/testutil"
)

const (
	RefreshPath           = "/v1/auth/refresh"
	SessionSignupBody     = `{"email":"sessions@example.com","password":"long enough password"}`
	UnknownRefreshToken   = "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	AuthFailedJSON        = `{"error":{"code":"authentication_failed","message":"Authentication failed"}}`
	InternalAuthErrorJSON = `{"error":{"code":"internal_error","message":"An internal error occurred"}}`
)

type HTTPTokenPair struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	TokenType    string `json:"token_type"`
	ExpiresIn    int    `json:"expires_in"`
}

type AuthHTTPResponse struct {
	Status int
	Header http.Header
	Body   string
}

func AuthHTTPRequest(
	t *testing.T,
	server *httptest.Server,
	method, path, body string,
	headers http.Header,
) AuthHTTPResponse {
	t.Helper()
	response, err := SendAuthHTTPRequest(t.Context(), server.Client(), method, server.URL+path, body, headers)
	require.NoError(t, err)
	return response
}

func SendAuthHTTPRequest(
	ctx context.Context,
	client *http.Client,
	method, url, body string,
	headers http.Header,
) (AuthHTTPResponse, error) {
	req, err := http.NewRequestWithContext(ctx, method, url, strings.NewReader(body))
	if err != nil {
		return AuthHTTPResponse{}, err
	}
	maps.Copy(req.Header, headers)
	if method == http.MethodPost {
		req.Header.Set("Content-Type", "application/json")
	}
	response, err := client.Do(req)
	if err != nil {
		return AuthHTTPResponse{}, err
	}
	defer response.Body.Close()
	data, err := io.ReadAll(response.Body)
	return AuthHTTPResponse{Status: response.StatusCode, Header: response.Header, Body: string(data)}, err
}

func Bearer(token string) http.Header {
	return http.Header{"Authorization": {"Bearer " + token}}
}

func AuthConfig(t *testing.T) application.Config {
	t.Helper()
	id, signing, verifying, _ := testutil.AuthKeys(t)
	return application.Config{
		Auth: application.AuthConfig{SigningKeyID: id, SigningKeyFile: signing, VerifyKeysFile: verifying},
	}
}

func NewAuthServer(t *testing.T, pool *pgxpool.Pool, config application.Config) *httptest.Server {
	t.Helper()
	keys := AuthConfig(t)
	config.Auth.SigningKeyID = keys.Auth.SigningKeyID
	config.Auth.SigningKeyFile = keys.Auth.SigningKeyFile
	config.Auth.VerifyKeysFile = keys.Auth.VerifyKeysFile
	app, err := application.New(t.Context(), config, application.WithPool(pool))
	require.NoError(t, err)
	t.Cleanup(app.Close)
	return httptest.NewServer(app.Routes())
}

func NewSessionServer(t *testing.T, pool *pgxpool.Pool) (*httptest.Server, func(time.Duration)) {
	t.Helper()
	var elapsed atomic.Int64
	start := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	server := NewAuthServer(t, pool, application.Config{Auth: application.AuthConfig{
		Now: func() time.Time { return start.Add(time.Duration(elapsed.Load())) },
	}})
	t.Cleanup(server.Close)
	return server, func(duration time.Duration) { elapsed.Store(int64(duration)) }
}

func StartHTTPSession(t *testing.T, server *httptest.Server) HTTPTokenPair {
	t.Helper()
	response := AuthHTTPRequest(t, server, http.MethodPost, "/v1/auth/signup", SessionSignupBody, nil)
	require.Equal(t, http.StatusCreated, response.Status)
	return LoginHTTPSession(t, server)
}

func LoginHTTPSession(t *testing.T, server *httptest.Server) HTTPTokenPair {
	t.Helper()
	return ReadHTTPTokenPair(t, AuthHTTPRequest(t, server, http.MethodPost, "/v1/auth/login", SessionSignupBody, nil))
}

func ReadHTTPTokenPair(t *testing.T, response AuthHTTPResponse) HTTPTokenPair {
	t.Helper()
	const (
		tokenResponseFields   = 4
		accessLifetimeSeconds = 900
		refreshTokenBytes     = 32
		refreshTokenLength    = 43
	)
	require.Equal(t, http.StatusOK, response.Status)
	assert.Equal(t, "no-store", response.Header.Get("Cache-Control"))
	assert.Equal(t, "application/json", response.Header.Get("Content-Type"))
	var fields map[string]json.RawMessage
	require.NoError(t, json.Unmarshal([]byte(response.Body), &fields))
	assert.Len(t, fields, tokenResponseFields)
	var pair HTTPTokenPair
	require.NoError(t, json.Unmarshal([]byte(response.Body), &pair))
	assert.Equal(t, "Bearer", pair.TokenType)
	assert.Equal(t, accessLifetimeSeconds, pair.ExpiresIn)
	assert.NotEmpty(t, pair.AccessToken)
	decoded, err := base64.RawURLEncoding.Strict().DecodeString(pair.RefreshToken)
	require.NoError(t, err)
	assert.Len(t, decoded, refreshTokenBytes)
	assert.Len(t, pair.RefreshToken, refreshTokenLength)
	return pair
}

func RefreshBody(secret string) string {
	return `{"refresh_token":"` + secret + `"}`
}

func PostRefreshToken(t *testing.T, server *httptest.Server, path, secret string) AuthHTTPResponse {
	t.Helper()
	return AuthHTTPRequest(t, server, http.MethodPost, path, RefreshBody(secret), nil)
}
