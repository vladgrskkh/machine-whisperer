package auth_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	handler "github.com/vladgrskkh/machine-whisperer/api/internal/handler/auth"
	"github.com/vladgrskkh/machine-whisperer/api/internal/handler/auth/authmock"
	"github.com/vladgrskkh/machine-whisperer/api/internal/handler/middleware"
	"github.com/vladgrskkh/machine-whisperer/api/internal/infrastructure/auth/password"
	"github.com/vladgrskkh/machine-whisperer/api/internal/usecase/auth"
)

func TestInvalidSignupDoesNotHash(t *testing.T) {
	t.Parallel()
	for _, body := range []string{
		`{"email":"a@example.com","password":"long enough password","unknown":1}`,
		`{"email":"a@example.com","password":"long enough password"} {}`,
		`{"email":"a@example.com","password":"long enough password"}` + strings.Repeat(" ", 8192),
		`{"email":"a@example.com","password":"long enough ` + string([]byte{0xff}) + ` password"}`,
		`{"email":`, `null`, `[]`, `{}`, `{"email":"invalid","password":"long enough password"}`,
		`{"email":"a@example.com","password":"short"}`,
	} {
		t.Run(body[:min(len(body), 60)], func(t *testing.T) {
			t.Parallel()
			hasher := authmock.NewPasswordHasher(t)
			server := httptest.NewServer(handler.New(auth.New(nil, hasher)))
			t.Cleanup(server.Close)
			response, err := server.Client().Post(server.URL, "application/json", strings.NewReader(body))
			require.NoError(t, err)
			defer response.Body.Close()
			assert.Equal(t, http.StatusBadRequest, response.StatusCode)
			var envelope struct {
				Error struct {
					Code string `json:"code"`
				} `json:"error"`
			}
			require.NoError(t, json.NewDecoder(response.Body).Decode(&envelope))
			assert.Equal(t, "invalid_input", envelope.Error.Code)
		})
	}
}

func TestSignupRejectsUnpairedSurrogatesBeforeHashing(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ name, password string }{
		{"lone high", `aaaaaaaaaaaaaa\ud800`},
		{"lone low", `aaaaaaaaaaaaaa\udc00`},
		{"reversed pair", `aaaaaaaaaaaaaa\udc00\ud800`},
		{"two high surrogates", `aaaaaaaaaaaaaa\ud800\udbff`},
		{"two low surrogates", `aaaaaaaaaaaaaa\udc00\udfff`},
		{"high then ordinary escape", `aaaaaaaaaaaaaa\ud800\u0061`},
		{"high then literal", `aaaaaaaaaaaaaa\ud800a`},
		{"high then replacement", `aaaaaaaaaaaaaa\ud800\ufffd`},
		{"high then escaped backslash", `aaaaaaaaaaaaaa\ud800\\udc00`},
		{"escaped backslash then high", `aaaaaaaaaaaaaa\\\ud800`},
		{"escaped quote then high", `aaaaaaaaaaaaaa\"\ud800`},
		{"pair then lone high", `aaaaaaaaaaaaaa\ud83d\ude00\ud800`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			hasher := authmock.NewPasswordHasher(t)
			hasher.EXPECT().Hash(mock.Anything, mock.Anything).
				Return("", errors.New("stop before persistence")).Maybe()
			server := httptest.NewServer(handler.New(auth.New(nil, hasher)))
			t.Cleanup(server.Close)
			body := `{"email":"a@example.com","password":"` + tc.password + `"}`
			response, err := server.Client().Post(server.URL, "application/json", strings.NewReader(body))
			require.NoError(t, err)
			defer response.Body.Close()
			assert.Equal(t, http.StatusBadRequest, response.StatusCode)
			var envelope struct {
				Error struct {
					Code string `json:"code"`
				} `json:"error"`
			}
			require.NoError(t, json.NewDecoder(response.Body).Decode(&envelope))
			assert.Equal(t, "invalid_input", envelope.Error.Code)
			hasher.AssertNotCalled(t, "Hash", mock.Anything, mock.Anything)
		})
	}
}

func TestSignupPreservesValidJSONPasswordContents(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ name, encoded, password string }{
		{"surrogate pair", `aaaaaaaaaaaaaa\ud83d\ude00`, "aaaaaaaaaaaaaa\U0001f600"},
		{"uppercase hex pair", `aaaaaaaaaaaaaa\uD83D\uDE00`, "aaaaaaaaaaaaaa\U0001f600"},
		{"lowest pair", `aaaaaaaaaaaaaa\ud800\udc00`, "aaaaaaaaaaaaaa\U00010000"},
		{"highest pair", `aaaaaaaaaaaaaa\udbff\udfff`, "aaaaaaaaaaaaaa\U0010ffff"},
		{"literal supplementary", "aaaaaaaaaaaaaa\U0001f600", "aaaaaaaaaaaaaa\U0001f600"},
		{"literal replacement", "aaaaaaaaaaaaaa\ufffd", "aaaaaaaaaaaaaa\ufffd"},
		{"escaped replacement", `aaaaaaaaaaaaaa\ufffd`, "aaaaaaaaaaaaaa\ufffd"},
		{"literal backslash u", `aaaaaaaaaaaaaa\\ud800`, `aaaaaaaaaaaaaa\ud800`},
		{"escaped backslash before pair", `aaaaaaaaaaaaaa\\\ud83d\ude00`, "aaaaaaaaaaaaaa\\\U0001f600"},
		{"ordinary JSON escapes", `  password\"\\\/\n\t\u0061  `, "  password\"\\/\n\ta  "},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			hasher := authmock.NewPasswordHasher(t)
			hasher.EXPECT().Hash(mock.Anything, tc.password).
				Return("", errors.New("stop before persistence")).Once()
			server := httptest.NewServer(handler.New(auth.New(nil, hasher)))
			t.Cleanup(server.Close)
			body := `{"email":"a@example.com","password":"` + tc.encoded + `"}`
			response, err := server.Client().Post(server.URL, "application/json", strings.NewReader(body))
			require.NoError(t, err)
			defer response.Body.Close()
			assert.Equal(t, http.StatusInternalServerError, response.StatusCode)
		})
	}
}

func TestRateLimitBeforePasswordHashing(t *testing.T) {
	t.Parallel()
	hasher := authmock.NewPasswordHasher(t)
	hasher.EXPECT().
		Hash(mock.Anything, "long enough password").
		Return("", errors.New("private cryptographic failure")).
		Once()
	server := httptest.NewServer(
		middleware.RateLimit(middleware.RateLimitConfig{Limit: 1})(handler.New(auth.New(nil, hasher))),
	)
	t.Cleanup(server.Close)
	for _, expected := range []int{http.StatusInternalServerError, http.StatusTooManyRequests} {
		response, err := server.Client().
			Post(server.URL, "application/json", strings.NewReader(`{"email":"a@example.com","password":"long enough password"}`))
		require.NoError(t, err)
		var result map[string]any
		require.NoError(t, json.NewDecoder(response.Body).Decode(&result))
		response.Body.Close()
		assert.Equal(t, expected, response.StatusCode)
		if expected == http.StatusInternalServerError {
			encoded, encodeErr := json.Marshal(result)
			require.NoError(t, encodeErr)
			assert.JSONEq(
				t,
				`{"error":{"code":"internal_error","message":"An internal error occurred"}}`,
				string(encoded),
			)
		}
	}
}

func TestSignupExpiredContextUsesErrorEnvelope(t *testing.T) {
	t.Parallel()
	signup := handler.New(auth.New(nil, password.Argon2id{}))
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithDeadline(r.Context(), time.Now().Add(-time.Second))
		defer cancel()
		signup.ServeHTTP(w, r.WithContext(ctx))
	}))
	t.Cleanup(server.Close)
	response, err := server.Client().
		Post(server.URL, "application/json", strings.NewReader(`{"email":"timeout@example.com","password":"long enough password"}`))
	require.NoError(t, err)
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	require.NoError(t, err)
	assert.Equal(t, http.StatusInternalServerError, response.StatusCode)
	assert.JSONEq(t, `{"error":{"code":"internal_error","message":"An internal error occurred"}}`, string(body))
}
