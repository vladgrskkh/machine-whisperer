//go:build integration

package integration_test

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"net/netip"
	"os"
	"sync/atomic"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"

	"github.com/vladgrskkh/machine-whisperer/api/internal/handler/middleware"
	"github.com/vladgrskkh/machine-whisperer/api/internal/infrastructure/auth/attempts"
	userpostgres "github.com/vladgrskkh/machine-whisperer/api/internal/infrastructure/user/postgres"

	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vladgrskkh/machine-whisperer/api/internal/application"
	"github.com/vladgrskkh/machine-whisperer/api/internal/testutil"
)

func TestLoginHTTP(t *testing.T) {
	t.Parallel()
	pool := testutil.Postgres(t)
	server := newAuthServer(t, pool, application.Config{})
	t.Cleanup(server.Close)
	post := func(path, body string) (int, http.Header, []byte) {
		t.Helper()
		response, err := server.Client().Post(server.URL+path, "application/json", strings.NewReader(body))
		require.NoError(t, err)
		defer response.Body.Close()
		data, err := io.ReadAll(response.Body)
		require.NoError(t, err)
		return response.StatusCode, response.Header, data
	}
	status, _, _ := post(
		"/v1/auth/signup",
		`{"email":"login@example.com","password":"  exact\ud83d\ude00 \ufffd \\ud800 password  "}`,
	)
	require.Equal(t, 201, status)
	status, headers, data := post(
		"/v1/auth/login",
		`{"email":" Login@Example.COM ","password":"  exact\ud83d\ude00 \ufffd \\ud800 password  "}`,
	)
	require.Equal(t, 200, status)
	assert.Equal(t, "no-store", headers.Get("Cache-Control"))
	var pair map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(data, &pair))
	require.Len(t, pair, 4)
	assert.JSONEq(t, `"Bearer"`, string(pair["token_type"]))
	assert.JSONEq(t, `900`, string(pair["expires_in"]))
	var access string
	require.NoError(t, json.Unmarshal(pair["access_token"], &access))
	assert.NotEmpty(t, pair["refresh_token"])
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, server.URL+"/v1/users/me", nil)
	require.NoError(t, err)
	req.Header.Set("Authorization", "bEaReR "+access)
	response, err := server.Client().Do(req)
	require.NoError(t, err)
	defer response.Body.Close()
	require.Equal(t, 200, response.StatusCode)
	var current map[string]json.RawMessage
	require.NoError(t, json.NewDecoder(response.Body).Decode(&current))
	assert.Len(t, current, 3)
	assert.JSONEq(t, `"login@example.com"`, string(current["email"]))
	assert.NotEmpty(t, current["id"])
	assert.NotEmpty(t, current["created_at"])
	for _, email := range []string{"login@example.com", "unknown@example.com"} {
		status, headers, data = post("/v1/auth/login", `{"email":"`+email+`","password":"wrong password material"}`)
		assert.Equal(t, 401, status)
		assert.Equal(t, "no-store", headers.Get("Cache-Control"))
		assert.JSONEq(t, `{"error":{"code":"authentication_failed","message":"Authentication failed"}}`, string(data))
	}
}

func TestProtectedRouteRequiresUserBearer(t *testing.T) {
	t.Parallel()
	pool := testutil.Postgres(t)
	server := newAuthServer(t, pool, application.Config{})
	t.Cleanup(server.Close)
	for _, value := range []string{"", "device-key", "Bearer device-key", "Basic abc", "Bearer a b", "Bearer " + strings.Repeat("x", 8193)} {
		req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, server.URL+"/v1/users/me", nil)
		require.NoError(t, err)
		req.Header.Set("Authorization", value)
		response, err := server.Client().Do(req)
		require.NoError(t, err)
		response.Body.Close()
		assert.Equal(t, 401, response.StatusCode)
		assert.Equal(t, "Bearer", response.Header.Get("WWW-Authenticate"))
	}
}

func authConfig(t *testing.T) application.Config {
	t.Helper()
	id, signing, verifying, _ := testutil.AuthKeys(t)
	return application.Config{SigningKeyID: id, SigningKeyFile: signing, VerifyKeysFile: verifying}
}

func newAuthServer(t *testing.T, pool *pgxpool.Pool, config application.Config) *httptest.Server {
	t.Helper()
	keys := authConfig(t)
	config.SigningKeyID, config.SigningKeyFile, config.VerifyKeysFile = keys.SigningKeyID, keys.SigningKeyFile, keys.VerifyKeysFile
	handler, err := application.NewHandler(t.Context(), pool, config)
	require.NoError(t, err)
	return httptest.NewServer(handler)
}

func TestLoginHTTPRejectsInvalidBodies(t *testing.T) {
	t.Parallel()
	pool := testutil.Postgres(t)
	server := newAuthServer(t, pool, application.Config{})
	t.Cleanup(server.Close)
	for _, body := range []string{
		`{`, `null`, `[]`, `"credentials"`, `{"email":1}`, `{"password":{}}`,
		`{"email":"body@example.com","password":"long enough password","extra":true}`,
		`{"email":"body@example.com","password":"long enough password"} {}`,
		`{"email":"body@example.com","password":"long enough password"}` + strings.Repeat(" ", 8192),
		`{"email":"body@example.com","password":"long enough password\ud800"}`,
		`{"email":"body@example.com","password":"long enough password\udc00"}`,
		"{\"email\":\"body@example.com\",\"password\":\"long enough password\xff\"}",
		`{"email":null,"password":"long enough password"}`,
		`{"email":"body@example.com","password":null}`,
		`{"email":"invalid","password":"long enough password"}`,
	} {
		response, err := server.Client().Post(server.URL+"/v1/auth/login", "application/json", strings.NewReader(body))
		require.NoError(t, err)
		data, err := io.ReadAll(response.Body)
		response.Body.Close()
		require.NoError(t, err)
		assert.Equal(t, http.StatusBadRequest, response.StatusCode)
		assert.Equal(t, "no-store", response.Header.Get("Cache-Control"))
		assert.JSONEq(
			t,
			`{"error":{"code":"invalid_input","message":"Invalid email, password or request body"}}`,
			string(data),
		)
	}
	_, err := pool.Exec(t.Context(), "DROP TABLE users CASCADE")
	require.NoError(t, err)
	response, err := server.Client().
		Post(server.URL+"/v1/auth/login", "application/json", strings.NewReader(`{"email":"body@example.com","password":"long enough password"}`))
	require.NoError(t, err)
	defer response.Body.Close()
	data, err := io.ReadAll(response.Body)
	require.NoError(t, err)
	assert.Equal(t, http.StatusInternalServerError, response.StatusCode)
	assert.Equal(t, "no-store", response.Header.Get("Cache-Control"))
	assert.JSONEq(t, `{"error":{"code":"internal_error","message":"An internal error occurred"}}`, string(data))
}

func TestSharedAuthIPBudget(t *testing.T) {
	t.Parallel()
	pool := testutil.Postgres(t)
	var seconds atomic.Int64
	now := func() time.Time { return time.Unix(seconds.Load(), 0) }
	server := newAuthServer(
		t,
		pool,
		application.Config{
			RateLimit: middleware.RateLimitConfig{
				Now:            now,
				TrustedProxies: []netip.Prefix{netip.MustParsePrefix("127.0.0.0/8"), netip.MustParsePrefix("::1/128")},
			},
		},
	)
	t.Cleanup(server.Close)
	for index := range 32 {
		path := "/v1/auth/signup"
		if index%2 != 0 {
			path = "/v1/auth/login"
		}
		body := `{}`
		if index >= 30 {
			body = `{"email":"limited@example.com","password":"long enough password"}`
		}
		req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, server.URL+path, strings.NewReader(body))
		require.NoError(t, err)
		req.Header.Add("X-Forwarded-For", strings.Repeat("spoof,", 2000)+"198.51.100.99")
		req.Header.Add("X-Forwarded-For", "198.51.100.8")
		response, err := server.Client().Do(req)
		require.NoError(t, err)
		response.Body.Close()
		want := 400
		if index >= 30 {
			want = 429
			assert.Equal(t, "60", response.Header.Get("Retry-After"))
		}
		assert.Equal(t, want, response.StatusCode)
		if path == "/v1/auth/login" {
			assert.Equal(t, "no-store", response.Header.Get("Cache-Control"))
		}
	}
	response, err := server.Client().Get(server.URL + "/v1/users/me")
	require.NoError(t, err)
	response.Body.Close()
	assert.Equal(t, 401, response.StatusCode)
	seconds.Store(60)
	req, err := http.NewRequestWithContext(
		t.Context(),
		http.MethodPost,
		server.URL+"/v1/auth/login",
		strings.NewReader(`{}`),
	)
	require.NoError(t, err)
	req.Header.Set("X-Forwarded-For", "198.51.100.8")
	response, err = server.Client().Do(req)
	require.NoError(t, err)
	response.Body.Close()
	assert.Equal(t, 400, response.StatusCode)
	var count int
	require.NoError(t, pool.QueryRow(t.Context(), "SELECT count(*) FROM users").Scan(&count))
	assert.Zero(t, count)
}

func TestLoginEmailBudgetHTTP(t *testing.T) {
	t.Parallel()
	pool := testutil.Postgres(t)
	var seconds atomic.Int64
	now := func() time.Time { return time.Unix(1800000000+seconds.Load(), 0) }
	server := newAuthServer(
		t,
		pool,
		application.Config{Now: now, EmailAttempts: attempts.Config{Limit: 2, Window: 10 * time.Second, Capacity: 1}},
	)
	t.Cleanup(server.Close)
	for _, tc := range []struct {
		email  string
		status int
	}{
		{" Unknown@Example.COM ", 401}, {"unknown@example.com", 401}, {"UNKNOWN@example.com", 429}, {"other@example.com", 429},
	} {
		response, err := server.Client().
			Post(server.URL+"/v1/auth/login", "application/json", strings.NewReader(`{"email":"`+tc.email+`","password":"long enough password"}`))
		require.NoError(t, err)
		response.Body.Close()
		assert.Equal(t, tc.status, response.StatusCode)
		assert.Equal(t, "no-store", response.Header.Get("Cache-Control"))
		if tc.status == 429 {
			assert.Equal(t, "10", response.Header.Get("Retry-After"))
		}
	}
	seconds.Store(10)
	response, err := server.Client().
		Post(server.URL+"/v1/auth/login", "application/json", strings.NewReader(`{"email":"other@example.com","password":"long enough password"}`))
	require.NoError(t, err)
	response.Body.Close()
	assert.Equal(t, 401, response.StatusCode)
}

func TestAccessJWTHTTP(t *testing.T) {
	t.Parallel()
	pool := testutil.Postgres(t)
	id, signing, verifying, private := testutil.AuthKeys(t)
	oldPublic, oldPrivate, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	public, ok := private.Public().(ed25519.PublicKey)
	require.True(t, ok)
	data, err := json.Marshal(
		map[string]string{
			"active": base64.StdEncoding.EncodeToString(public),
			"old":    base64.StdEncoding.EncodeToString(oldPublic),
		},
	)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(verifying, data, 0o600))
	now := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	handler, err := application.NewHandler(
		t.Context(),
		pool,
		application.Config{
			SigningKeyID:   id,
			SigningKeyFile: signing,
			VerifyKeysFile: verifying,
			Now:            func() time.Time { return now },
		},
	)
	require.NoError(t, err)
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	created, err := userpostgres.New(pool).Create(t.Context(), "access@example.com", "unused password hash")
	require.NoError(t, err)
	claims := func() jwt.MapClaims {
		return jwt.MapClaims{
			"iss": "machine-whisperer",
			"aud": "machine-whisperer-api",
			"sub": created.ID,
			"sid": uuid.NewString(),
			"jti": uuid.NewString(),
			"iat": now.Unix(),
			"exp": now.Add(15 * time.Minute).Unix(),
		}
	}
	signed := func(values jwt.MapClaims, kid string, key any, method jwt.SigningMethod) string {
		t.Helper()
		value := jwt.NewWithClaims(method, values)
		if kid != "" {
			value.Header["kid"] = kid
		}
		raw, signErr := value.SignedString(key)
		require.NoError(t, signErr)
		return raw
	}
	request := func(raw string, extra ...string) (int, []byte) {
		t.Helper()
		req, reqErr := http.NewRequestWithContext(t.Context(), http.MethodGet, server.URL+"/v1/users/me", nil)
		require.NoError(t, reqErr)
		req.Header.Set("Authorization", raw)
		req.Header.Set("X-Api-Key", "device-key")
		for _, value := range extra {
			req.Header.Add("Authorization", value)
		}
		response, requestErr := server.Client().Do(req)
		require.NoError(t, requestErr)
		defer response.Body.Close()
		body, readErr := io.ReadAll(response.Body)
		require.NoError(t, readErr)
		if response.StatusCode == http.StatusUnauthorized {
			assert.Equal(t, "Bearer", response.Header.Get("WWW-Authenticate"))
			assert.JSONEq(
				t,
				`{"error":{"code":"authentication_failed","message":"Authentication failed"}}`,
				string(body),
			)
		}
		return response.StatusCode, body
	}
	valid := signed(claims(), "active", private, jwt.SigningMethodEdDSA)
	status, _ := request("Bearer " + valid)
	assert.Equal(t, 200, status)
	status, _ = request("bEaReR " + signed(claims(), "old", oldPrivate, jwt.SigningMethodEdDSA))
	assert.Equal(t, 200, status)
	for _, header := range []string{"Bearer " + valid + "," + valid, "Bearer  " + valid, "Bearer\t" + valid, "Bearer " + valid + " extra"} {
		status, _ = request(header)
		assert.Equal(t, 401, status)
	}
	status, _ = request("Bearer "+valid, "Bearer "+valid)
	assert.Equal(t, 401, status)
	status, _ = request("Bearer "+valid, "")
	assert.Equal(t, 401, status)
	for _, field := range []string{"iss", "aud", "sub", "sid", "jti", "iat", "exp"} {
		value := claims()
		delete(value, field)
		status, _ = request("Bearer " + signed(value, "active", private, jwt.SigningMethodEdDSA))
		assert.Equal(t, 401, status, field)
	}
	for _, tc := range []struct {
		field string
		value any
	}{
		{"iss", "other"}, {"aud", "other"}, {"aud", []string{"machine-whisperer-api", "other"}},
		{"sub", "device-key"}, {"sid", "invalid"}, {"jti", "invalid"}, {"iat", "invalid"},
		{"iat", now.Add(31 * time.Second).Unix()}, {"exp", now.Add(-31 * time.Second).Unix()},
	} {
		value := claims()
		value[tc.field] = tc.value
		if tc.field == "iat" && tc.value == now.Add(31*time.Second).Unix() {
			value["exp"] = now.Add(15*time.Minute + 31*time.Second).Unix()
		}
		if tc.field == "exp" {
			value["iat"] = now.Add(-15*time.Minute - 31*time.Second).Unix()
		}
		status, _ = request("Bearer " + signed(value, "active", private, jwt.SigningMethodEdDSA))
		assert.Equal(t, 401, status, tc.field)
	}
	for _, raw := range []string{
		signed(claims(), "unknown", private, jwt.SigningMethodEdDSA),
		signed(claims(), "", private, jwt.SigningMethodEdDSA),
		signed(claims(), "active", oldPrivate, jwt.SigningMethodEdDSA),
		signed(claims(), "active", []byte("test HMAC key"), jwt.SigningMethodHS256),
	} {
		status, _ = request("Bearer " + raw)
		assert.Equal(t, 401, status)
	}
	// No session row exists for these signed claims: access validation is stateless.
	for _, offset := range []time.Duration{30 * time.Second, -15*time.Minute - 29*time.Second} {
		value := claims()
		value["iat"] = now.Add(offset).Unix()
		value["exp"] = now.Add(offset + 15*time.Minute).Unix()
		status, _ = request("Bearer " + signed(value, "active", private, jwt.SigningMethodEdDSA))
		assert.Equal(t, 200, status)
	}
	_, err = pool.Exec(t.Context(), "DELETE FROM users WHERE id=$1", created.ID)
	require.NoError(t, err)
	status, _ = request("Bearer " + valid)
	assert.Equal(t, 401, status)
	_, err = pool.Exec(t.Context(), "DROP TABLE users CASCADE")
	require.NoError(t, err)
	status, data = request("Bearer " + valid)
	assert.Equal(t, 500, status)
	assert.JSONEq(t, `{"error":{"code":"internal_error","message":"An internal error occurred"}}`, string(data))
}

func TestLoginMethodErrorsDoNotCache(t *testing.T) {
	t.Parallel()
	pool := testutil.Postgres(t)
	server := newAuthServer(t, pool, application.Config{})
	t.Cleanup(server.Close)
	response, err := server.Client().Get(server.URL + "/v1/auth/login")
	require.NoError(t, err)
	defer response.Body.Close()
	assert.Equal(t, http.StatusMethodNotAllowed, response.StatusCode)
	assert.Equal(t, "no-store", response.Header.Get("Cache-Control"))
}
