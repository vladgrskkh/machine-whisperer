package telemetry_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/vladgrskkh/machine-whisperer/api/internal/domain/machine"
	"github.com/vladgrskkh/machine-whisperer/api/internal/domain/session"
	"github.com/vladgrskkh/machine-whisperer/api/internal/handler/middleware"
	telemetryhandler "github.com/vladgrskkh/machine-whisperer/api/internal/handler/telemetry"
	"github.com/vladgrskkh/machine-whisperer/api/internal/infrastructure/auth/token"
	"github.com/vladgrskkh/machine-whisperer/api/internal/infrastructure/telemetry/analytics"
	"github.com/vladgrskkh/machine-whisperer/api/internal/testutil"
	"github.com/vladgrskkh/machine-whisperer/api/internal/usecase/telemetry"
	"github.com/vladgrskkh/machine-whisperer/api/internal/usecase/telemetry/telemetrymock"
)

func TestHistoryOwnershipLookupFailure(t *testing.T) {
	t.Parallel()
	var calls atomic.Int64
	downstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		fmt.Fprint(w, `{"items":[],"next_cursor":null}`)
	}))
	t.Cleanup(downstream.Close)

	ownerID, machineID := uuid.NewString(), uuid.NewString()
	keyID, signingFile, verifyFile, _ := testutil.AuthKeys(t)
	keys, err := token.LoadKeys(keyID, signingFile, verifyFile)
	require.NoError(t, err)
	now := time.Date(2026, time.November, 24, 12, 0, 0, 0, time.UTC)
	jwt, err := token.NewJWT(keys, func() time.Time { return now })
	require.NoError(t, err)
	access, err := jwt.Sign(t.Context(), session.AccessClaims{
		UserID: ownerID, SessionID: uuid.NewString(), JTI: uuid.NewString(),
		IssuedAt: now, ExpiresAt: now.Add(15 * time.Minute),
	})
	require.NoError(t, err)

	const privateDetail = "private PostgreSQL lookup detail"
	for _, tc := range []struct {
		name   string
		err    error
		status int
		code   string
	}{
		{"database_error", errors.New(privateDetail), http.StatusServiceUnavailable, "telemetry_unavailable"},
		{"database_timeout", fmt.Errorf("%s: %w", privateDetail, context.DeadlineExceeded),
			http.StatusServiceUnavailable, "telemetry_unavailable"},
		{"not_found", fmt.Errorf("%s: %w", privateDetail, machine.ErrNotFound),
			http.StatusNotFound, "machine_not_found"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			repository := telemetrymock.NewHistoryMachineRepository(t)
			repository.EXPECT().FindByOwner(mock.Anything, ownerID, machineID).
				Return(machine.Machine{}, tc.err).Once()
			client := analytics.NewHistoryClient(downstream.URL, "test-analytics-key", http.DefaultTransport)
			list := telemetry.NewListHistory(repository, client)
			router := chi.NewRouter()
			router.With(middleware.AccessToken(jwt)).
				Get("/v1/machines/{machine_id}/telemetry", telemetryhandler.NewHistory(list).ServeHTTP)
			server := httptest.NewServer(router)
			t.Cleanup(server.Close)

			request, err := http.NewRequestWithContext(t.Context(), http.MethodGet,
				server.URL+"/v1/machines/"+machineID+"/telemetry", nil)
			require.NoError(t, err)
			request.Header.Set("Authorization", "Bearer "+access)
			response, err := server.Client().Do(request)
			require.NoError(t, err)
			defer response.Body.Close()
			body, err := io.ReadAll(response.Body)
			require.NoError(t, err)
			var envelope struct {
				Error struct {
					Code string `json:"code"`
				} `json:"error"`
			}
			require.NoError(t, json.Unmarshal(body, &envelope))
			assert.Equal(t, tc.status, response.StatusCode)
			assert.Equal(t, tc.code, envelope.Error.Code)
			assert.NotContains(t, string(body), privateDetail)
			assert.Zero(t, calls.Load())
		})
	}
}
