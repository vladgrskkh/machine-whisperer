//go:build integration

package auth_test

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vladgrskkh/machine-whisperer/api/internal/application"
	"github.com/vladgrskkh/machine-whisperer/api/internal/handler/middleware"
	userpostgres "github.com/vladgrskkh/machine-whisperer/api/internal/infrastructure/user/postgres"
	"github.com/vladgrskkh/machine-whisperer/api/internal/testutil"
	"github.com/vladgrskkh/machine-whisperer/api/tests/integration/internal/testfixture"
)

func TestLoginHTTP(t *testing.T) {
	t.Parallel()
	pool := testutil.Postgres(t)
	server := testfixture.NewAuthServer(t, pool, application.Config{})
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
	server := testfixture.NewAuthServer(t, pool, application.Config{})
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

func TestLoginHTTPRejectsInvalidBodies(t *testing.T) {
	t.Parallel()
	pool := testutil.Postgres(t)
	server := testfixture.NewAuthServer(t, pool, application.Config{})
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
	server := testfixture.NewAuthServer(
		t,
		pool,
		application.Config{
			RateLimits: application.RateLimitsConfig{IP: middleware.RateLimitConfig{
				Now:            now,
				TrustedProxies: []netip.Prefix{netip.MustParsePrefix("127.0.0.0/8"), netip.MustParsePrefix("::1/128")},
			}},
		},
	)
	t.Cleanup(server.Close)
	pair := testfixture.StartHTTPSession(t, server)
	logoutPair := testfixture.LoginHTTPSession(t, server)
	paths := []string{"/v1/auth/signup", "/v1/auth/login", testfixture.RefreshPath}
	headers := func(spoof string) http.Header {
		return http.Header{"X-Forwarded-For": {strings.Repeat("spoof,", 2000) + spoof, "198.51.100.8"}}
	}
	for index := range 30 {
		path := paths[index%len(paths)]
		spoof := fmt.Sprintf("192.0.2.%d", index+1)
		response := testfixture.AuthHTTPRequest(t, server, http.MethodPost, path, `{}`, headers(spoof))
		assert.Equal(t, http.StatusBadRequest, response.Status)
	}
	for _, request := range []struct{ path, body string }{
		{testfixture.RefreshPath, testfixture.RefreshBody(pair.RefreshToken)},
		{"/v1/auth/signup", `{"email":"limited@example.com","password":"long enough password"}`},
		{"/v1/auth/login", testfixture.SessionSignupBody},
	} {
		response := testfixture.AuthHTTPRequest(
			t,
			server,
			http.MethodPost,
			request.path,
			request.body,
			headers("198.51.100.99"),
		)
		assert.Equal(t, http.StatusTooManyRequests, response.Status, request.path)
		assert.Equal(t, "60", response.Header.Get("Retry-After"))
		assert.JSONEq(t, `{"error":{"code":"rate_limited","message":"Too many requests"}}`, response.Body)
		if request.path != "/v1/auth/signup" {
			assert.Equal(t, "no-store", response.Header.Get("Cache-Control"))
		}
	}
	otherIP := testfixture.AuthHTTPRequest(
		t, server, http.MethodPost, testfixture.RefreshPath, `{}`, http.Header{"X-Forwarded-For": {"198.51.100.9"}},
	)
	assert.Equal(t, http.StatusBadRequest, otherIP.Status)
	logout := testfixture.AuthHTTPRequest(
		t,
		server,
		http.MethodPost,
		logoutPath,
		testfixture.RefreshBody(logoutPair.RefreshToken),
		headers("192.0.2.5"),
	)
	assertHTTPLogout(t, logout)
	me := testfixture.AuthHTTPRequest(t, server, http.MethodGet, "/v1/users/me", "", headers("192.0.2.5"))
	assert.Equal(t, http.StatusUnauthorized, me.Status)
	state := readRefreshSession(t, pool, httpSessionID(t, pair))
	assert.Len(t, state.tokens, 1)
	oldHash := refreshHash(t, pair.RefreshToken)
	assert.Contains(t, state.tokens, oldHash)
	assert.Nil(t, state.tokens[oldHash])
	var users, sessions int
	require.NoError(
		t,
		pool.QueryRow(t.Context(), "SELECT (SELECT count(*) FROM users), (SELECT count(*) FROM sessions)").
			Scan(&users, &sessions),
	)
	assert.Equal(t, 1, users)
	assert.Equal(t, 2, sessions)

	seconds.Store(60)
	response := testfixture.AuthHTTPRequest(
		t,
		server,
		http.MethodPost,
		testfixture.RefreshPath,
		testfixture.RefreshBody(pair.RefreshToken),
		headers("192.0.2.6"),
	)
	testfixture.ReadHTTPTokenPair(t, response)
}

func TestLoginFailuresUseIPBudget(t *testing.T) {
	t.Parallel()
	pool := testutil.Postgres(t)
	server := testfixture.NewAuthServer(t, pool, application.Config{})
	t.Cleanup(server.Close)

	for range 6 {
		response, err := server.Client().Post(
			server.URL+"/v1/auth/login", "application/json",
			strings.NewReader(`{"email":"unknown@example.com","password":"long enough password"}`),
		)
		require.NoError(t, err)
		response.Body.Close()
		assert.Equal(t, http.StatusUnauthorized, response.StatusCode)
		assert.Equal(t, "no-store", response.Header.Get("Cache-Control"))
	}
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
	app, err := application.New(
		t.Context(),
		application.Config{Auth: application.AuthConfig{
			SigningKeyID:   id,
			SigningKeyFile: signing,
			VerifyKeysFile: verifying,
			Now:            func() time.Time { return now },
		}},
		application.WithPool(pool),
	)
	require.NoError(t, err)
	t.Cleanup(app.Close)
	server := httptest.NewServer(app.Routes())
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
	server := testfixture.NewAuthServer(t, pool, application.Config{})
	t.Cleanup(server.Close)
	response, err := server.Client().Get(server.URL + "/v1/auth/login")
	require.NoError(t, err)
	defer response.Body.Close()
	assert.Equal(t, http.StatusMethodNotAllowed, response.StatusCode)
	assert.Equal(t, "no-store", response.Header.Get("Cache-Control"))
}

func TestAccessJWTsRemainValidAfterLogoutAndRefreshReuse(t *testing.T) {
	t.Parallel()
	pool := testutil.Postgres(t)
	server, setElapsed := testfixture.NewSessionServer(t, pool)
	loggedOut := testfixture.StartHTTPSession(t, server)
	reused := testfixture.LoginHTTPSession(t, server)
	assertHTTPLogout(t, testfixture.PostRefreshToken(t, server, logoutPath, loggedOut.RefreshToken))
	testfixture.ReadHTTPTokenPair(
		t,
		testfixture.PostRefreshToken(t, server, testfixture.RefreshPath, reused.RefreshToken),
	)
	assertRefreshHTTPRejected(t, testfixture.PostRefreshToken(t, server, testfixture.RefreshPath, reused.RefreshToken))

	getMe := func(raw string) testfixture.AuthHTTPResponse {
		t.Helper()
		headers := http.Header{"Authorization": {"Bearer " + raw}}
		return testfixture.AuthHTTPRequest(t, server, http.MethodGet, "/v1/users/me", "", headers)
	}
	assertUnauthorized := func(response testfixture.AuthHTTPResponse) {
		t.Helper()
		assert.Equal(t, http.StatusUnauthorized, response.Status)
		assert.Equal(t, "Bearer", response.Header.Get("WWW-Authenticate"))
		assert.JSONEq(t, testfixture.AuthFailedJSON, response.Body)
	}
	assertUnauthorized(getMe(loggedOut.RefreshToken))
	assertUnauthorized(getMe("device-key"))
	for _, elapsed := range []time.Duration{0, 15 * time.Minute, 15*time.Minute + 29*time.Second} {
		setElapsed(elapsed)
		assert.Equal(t, http.StatusOK, getMe(loggedOut.AccessToken).Status, elapsed)
		assert.Equal(t, http.StatusOK, getMe(reused.AccessToken).Status, elapsed)
	}
	setElapsed(15*time.Minute + 30*time.Second)
	assertUnauthorized(getMe(loggedOut.AccessToken))
	assertUnauthorized(getMe(reused.AccessToken))

	deleted := testfixture.LoginHTTPSession(t, server)
	require.Equal(t, http.StatusOK, getMe(deleted.AccessToken).Status)
	_, err := pool.Exec(t.Context(), "DELETE FROM users")
	require.NoError(t, err)
	assertUnauthorized(getMe(deleted.AccessToken))
}
