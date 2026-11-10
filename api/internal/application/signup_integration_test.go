//go:build integration

package application_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"

	"sync"
	"testing"

	"github.com/vladgrskkh/machine-whisperer/api/internal/application"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vladgrskkh/machine-whisperer/api/internal/domain/user"
	"github.com/vladgrskkh/machine-whisperer/api/internal/handler/middleware"
	"github.com/vladgrskkh/machine-whisperer/api/internal/infrastructure/auth/password"
	"github.com/vladgrskkh/machine-whisperer/api/internal/infrastructure/user/postgres"
	"github.com/vladgrskkh/machine-whisperer/api/internal/testutil"
	"github.com/vladgrskkh/machine-whisperer/api/internal/usecase/auth"
)

func TestAccountCreation(t *testing.T) {
	t.Parallel()
	pool := testutil.Postgres(t)
	hasher := password.Argon2id{}
	signup := auth.New(postgres.New(pool), hasher)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	for _, credentials := range []struct{ email, password string }{
		{"invalid", "long enough password"},
		{"valid@example.com", "short"},
	} {
		_, err := signup.Signup(ctx, credentials.email, credentials.password)
		assert.ErrorIs(t, err, user.ErrInvalidCredentials)
	}
	var initialCount int
	require.NoError(t, pool.QueryRow(t.Context(), "SELECT count(*) FROM users").Scan(&initialCount))
	assert.Zero(t, initialCount)
	created, err := signup.Signup(t.Context(), "  Alice@EXAMPLE.COM  ", "  exact 界 password  ")
	require.NoError(t, err)
	assert.NotEmpty(t, created.ID)
	assert.Equal(t, "alice@example.com", created.Email)
	assert.False(t, created.CreatedAt.IsZero())
	var hash string
	require.NoError(
		t,
		pool.QueryRow(t.Context(), "SELECT password_hash FROM users WHERE id=$1", created.ID).Scan(&hash),
	)
	ok, err := hasher.Verify(t.Context(), "  exact 界 password  ", hash)
	require.NoError(t, err)
	assert.True(t, ok)
	var wg sync.WaitGroup
	for range 5 {
		wg.Go(func() {
			_, signupErr := signup.Signup(t.Context(), "ALICE@example.com", "  exact 界 password  ")
			assert.ErrorIs(t, signupErr, user.ErrEmailExists)
		})
	}
	wg.Wait()
	var count int
	require.NoError(t, pool.QueryRow(t.Context(), "SELECT count(*) FROM users").Scan(&count))
	assert.Equal(t, 1, count)
}

func TestConcurrentSignup(t *testing.T) {
	t.Parallel()
	pool := testutil.Postgres(t)
	signup := auth.New(postgres.New(pool), password.Argon2id{})
	results := make(chan error, 6)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for _, email := range []string{
		"concurrent@example.com", "CONCURRENT@example.com", "  concurrent@example.com  ",
		"Concurrent@EXAMPLE.COM", "concurrent@EXAMPLE.COM", " Concurrent@example.com ",
	} {
		wg.Go(func() {
			<-start
			_, err := signup.Signup(t.Context(), email, "long enough password")
			results <- err
		})
	}
	close(start)
	wg.Wait()
	close(results)
	successes := 0
	for err := range results {
		if err == nil {
			successes++
		} else {
			assert.ErrorIs(t, err, user.ErrEmailExists)
		}
	}
	assert.Equal(t, 1, successes)
	var count int
	require.NoError(t, pool.QueryRow(t.Context(), "SELECT count(*) FROM users").Scan(&count))
	assert.Equal(t, 1, count)
}

//nolint:tparallel // The database failure check must follow all signup error cases.
func TestSignupHTTP(t *testing.T) {
	t.Parallel()
	pool := testutil.Postgres(t)
	server := httptest.NewServer(application.NewHandler(pool, application.Config{}))
	t.Cleanup(server.Close)
	request := func(t *testing.T, body string) (int, map[string]json.RawMessage) {
		t.Helper()
		response, err := server.Client().Post(server.URL+"/v1/auth/signup", "application/json", strings.NewReader(body))
		require.NoError(t, err)
		defer response.Body.Close()
		var result map[string]json.RawMessage
		require.NoError(t, json.NewDecoder(response.Body).Decode(&result))
		return response.StatusCode, result
	}
	status, result := request(t, `{"email":"  Bob@Example.COM ","password":"  exact 界 password  "}`)
	assert.Equal(t, http.StatusCreated, status)
	assert.Len(t, result, 3)
	assert.JSONEq(t, `"bob@example.com"`, string(result["email"]))
	assert.NotEmpty(t, result["id"])
	assert.NotEmpty(t, result["created_at"])
	var storedHash string
	require.NoError(
		t,
		pool.QueryRow(t.Context(), "SELECT password_hash FROM users WHERE email=$1", "bob@example.com").
			Scan(&storedHash),
	)
	matches, err := (password.Argon2id{}).Verify(t.Context(), "  exact 界 password  ", storedHash)
	require.NoError(t, err)
	assert.True(t, matches)
	cases := []struct {
		name, body, code string
		status           int
	}{
		{"duplicate", `{"email":"BOB@example.com","password":"long enough password"}`, "email_exists", 409},
		{"validation", `{"email":"invalid","password":"long enough password"}`, "invalid_input", 400},
		{"short password", `{"email":"other@example.com","password":"short"}`, "invalid_input", 400},
		{"unknown", `{"email":"other@example.com","password":"long enough password","extra":1}`, "invalid_input", 400},
		{"trailing", `{"email":"other@example.com","password":"long enough password"} {}`, "invalid_input", 400},
		{"malformed", `{"email":`, "invalid_input", 400},
		{"null", `null`, "invalid_input", 400},
		{
			"oversized",
			`{"email":"other@example.com","password":"long enough password"}` + strings.Repeat(" ", 8192),
			"invalid_input",
			400,
		},
	}
	t.Run("errors", func(t *testing.T) {
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				t.Parallel()
				caseStatus, caseResult := request(t, tc.body)
				assert.Equal(t, tc.status, caseStatus)
				var envelope struct {
					Code string `json:"code"`
				}
				require.NoError(t, json.Unmarshal(caseResult["error"], &envelope))
				assert.Equal(t, tc.code, envelope.Code)
			})
		}
	})
	var count int
	require.NoError(t, pool.QueryRow(t.Context(), "SELECT count(*) FROM users").Scan(&count))
	assert.Equal(t, 1, count)
	_, err = pool.Exec(t.Context(), "DROP TABLE users")
	require.NoError(t, err)
	status, result = request(t, `{"email":"failure@example.com","password":"long enough password"}`)
	assert.Equal(t, http.StatusInternalServerError, status)
	assert.JSONEq(t, `{"code":"internal_error","message":"An internal error occurred"}`, string(result["error"]))
}

func TestNewPool(t *testing.T) {
	t.Parallel()
	existing := testutil.Postgres(t)
	pool, err := application.NewPool(t.Context(), application.Config{DatabaseURL: existing.Config().ConnString()})
	require.NoError(t, err)
	require.NotNil(t, pool)
	t.Cleanup(pool.Close)
	require.NoError(t, pool.Ping(t.Context()))
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err = application.NewPool(ctx, application.Config{DatabaseURL: existing.Config().ConnString()})
	assert.Error(t, err)
	_, err = application.NewPool(t.Context(), application.Config{DatabaseURL: "://invalid"})
	assert.Error(t, err)
}

func TestSignupRateLimit(t *testing.T) {
	t.Parallel()
	pool := testutil.Postgres(t)
	server := httptest.NewServer(
		application.NewHandler(pool, application.Config{RateLimit: middleware.RateLimitConfig{Limit: 1}}),
	)
	t.Cleanup(server.Close)
	for _, tc := range []struct {
		body string
		want int
	}{
		{`{}`, 400}, {`{"email":"limited@example.com","password":"long enough password"}`, 429},
	} {
		response, err := server.Client().
			Post(server.URL+"/v1/auth/signup", "application/json", strings.NewReader(tc.body))
		require.NoError(t, err)
		response.Body.Close()
		assert.Equal(t, tc.want, response.StatusCode)
	}
	var count int
	require.NoError(t, pool.QueryRow(t.Context(), "SELECT count(*) FROM users").Scan(&count))
	assert.Zero(t, count)
}
