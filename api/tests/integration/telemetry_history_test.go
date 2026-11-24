//go:build integration

package integration_test

import (
	"encoding/base64"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vladgrskkh/machine-whisperer/api/internal/application"
	"github.com/vladgrskkh/machine-whisperer/api/internal/testutil"
)

const (
	historyAnalyticsKey = "internal-test-analytics-key"
	emptyHistoryPage    = `{"items":[],"next_cursor":null}`
)

func TestTelemetryHistoryOwnerAccess(t *testing.T) {
	t.Parallel()
	var calls atomic.Int64
	analytics := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		assert.Equal(t, http.MethodGet, r.Method)
		assert.Equal(t, "Bearer "+historyAnalyticsKey, r.Header.Get("Authorization"))
		assert.Equal(t, "limit=100", r.URL.RawQuery)
		fmt.Fprint(w, emptyHistoryPage)
	}))
	t.Cleanup(analytics.Close)
	server, owner, machineID := startHistoryServer(t, analytics.URL)
	_, other := signupEnrollmentUser(t, server, `{"email":"other@example.com","password":"long enough password"}`)

	path := historyPath(machineID)
	unauthenticated := authHTTPRequest(t, server, http.MethodGet, path, "", nil)
	assert.Equal(t, http.StatusUnauthorized, unauthenticated.status)
	foreign := authHTTPRequest(t, server, http.MethodGet, path, "", bearer(other.AccessToken))
	missing := authHTTPRequest(t, server, http.MethodGet, historyPath(uuid.NewString()), "", owner)
	assert.Equal(t, http.StatusNotFound, foreign.status)
	assert.Equal(t, http.StatusNotFound, missing.status)
	assert.JSONEq(t, missing.body, foreign.body)
	assert.Zero(t, calls.Load())

	response := authHTTPRequest(t, server, http.MethodGet, path, "", owner)
	require.Equal(t, http.StatusOK, response.status, response.body)
	assert.Equal(t, "no-store", response.header.Get("Cache-Control"))
	assert.Equal(t, "application/json", response.header.Get("Content-Type"))
	assert.JSONEq(t, emptyHistoryPage, response.body)
	assert.EqualValues(t, 1, calls.Load())
}

func TestTelemetryHistoryRejectsInvalidQuery(t *testing.T) {
	t.Parallel()
	var calls atomic.Int64
	analytics := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		fmt.Fprint(w, emptyHistoryPage)
	}))
	t.Cleanup(analytics.Close)
	server, owner, machineID := startHistoryServer(t, analytics.URL)
	path := historyPath(machineID)

	for _, target := range []string{
		historyPath("not-a-uuid"),
		path + "?limit=0", path + "?limit=-1", path + "?limit=1001", path + "?limit=not-a-number",
		path + "?limit=999999999999999999999", path + "?limit=", path + "?limit=1&limit=2",
		path + "?limit=%", path + "?cursor=", path + "?cursor=not%2Fbase64",
		path + "?cursor=e30", path + "?cursor=e30&cursor=e30",
		path + "?cursor=" + historyCursor(uuid.NewString()),
		path + "?cursor=" + strings.Repeat("A", 513),
	} {
		response := authHTTPRequest(t, server, http.MethodGet, target, "", owner)
		assert.Equal(t, http.StatusBadRequest, response.status, target)
		assert.Contains(t, response.body, `"invalid_input"`, target)
	}
	assert.Zero(t, calls.Load())
}

func TestTelemetryHistoryAnalyticsFailureIsSanitized(t *testing.T) {
	t.Parallel()
	const sensitive = "private analytics error"
	var redirects atomic.Int64
	analytics := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/redirect-target" {
			redirects.Add(1)
			fmt.Fprint(w, emptyHistoryPage)
			return
		}
		switch r.URL.Query().Get("limit") {
		case "1":
			http.Error(w, sensitive, http.StatusBadRequest)
		case "2":
			http.Error(w, sensitive, http.StatusUnauthorized)
		case "3":
			http.Error(w, sensitive, http.StatusInternalServerError)
		case "4":
			http.Redirect(w, r, "/redirect-target", http.StatusTemporaryRedirect)
		case "5":
			fmt.Fprint(w, sensitive)
		default:
			fmt.Fprint(w, emptyHistoryPage)
		}
	}))
	t.Cleanup(analytics.Close)
	server, owner, machineID := startHistoryServer(t, analytics.URL)
	path := historyPath(machineID)

	for _, target := range []string{"?limit=1", "?limit=2", "?limit=3", "?limit=4", "?limit=5", ""} {
		if target == "" {
			analytics.Close()
		}
		response := authHTTPRequest(t, server, http.MethodGet, path+target, "", owner)
		assert.NotContains(t, response.body, sensitive)
		assert.NotContains(t, response.body, analytics.URL)
		assert.NotContains(t, response.body, historyAnalyticsKey)
		if target == "?limit=1" {
			assert.Equal(t, http.StatusBadRequest, response.status)
			assert.Contains(t, response.body, `"invalid_input"`)
		} else {
			assert.Equal(t, http.StatusServiceUnavailable, response.status)
			assert.Contains(t, response.body, `"telemetry_unavailable"`)
		}
	}
	assert.Zero(t, redirects.Load())
}

func TestTelemetryHistoryCursorForwarding(t *testing.T) {
	t.Parallel()
	const items = `[{
		"device_id":"77777777-7777-4777-8777-777777777777",
		"event_id":"88888888-8888-4888-8888-888888888888",
		"received_at":"2026-11-24T12:01:00.987654Z",
		"schema_version":1,
		"observed_at":"2026-11-24T12:00:00.123456Z",
		"operating_state":"running",
		"vibration_rms_g":0.043,
		"dominant_frequency_hz":null,
		"current_rms_a":null
	},{
		"device_id":"77777777-7777-4777-8777-777777777777",
		"event_id":"99999999-9999-4999-8999-999999999999",
		"received_at":"2026-11-24T12:01:00.987654Z",
		"schema_version":1,
		"observed_at":"2026-11-24T11:59:59.123456Z",
		"operating_state":"running",
		"vibration_rms_g":0.048,
		"dominant_frequency_hz":50.25,
		"current_rms_a":2.5
	}]`
	var receivedPath, receivedCursor atomic.Value
	var downstreamCursor atomic.Pointer[string]
	analytics := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedPath.Store(r.URL.Path)
		receivedCursor.Store(r.URL.Query().Get("cursor"))
		assert.Equal(t, "1000", r.URL.Query().Get("limit"))
		fmt.Fprintf(w, `{"items":%s,"next_cursor":%q}`, items, *downstreamCursor.Load())
	}))
	t.Cleanup(analytics.Close)
	server, owner, machineID := startHistoryServer(t, analytics.URL)
	cursor := historyCursor(machineID)
	next := historyCursorAt(machineID, "2026-11-24T11:59:59.123456Z", "99999999-9999-4999-8999-999999999999")
	downstreamCursor.Store(&next)
	response := authHTTPRequest(
		t, server, http.MethodGet, historyPath(machineID)+"?limit=1000&cursor="+cursor, "", owner,
	)
	require.Equal(t, http.StatusOK, response.status, response.body)
	assert.Equal(t, "/internal/v1/machines/"+machineID+"/telemetry", receivedPath.Load())
	assert.Equal(t, cursor, receivedCursor.Load())
	assert.JSONEq(t, fmt.Sprintf(`{"items":%s,"next_cursor":%q}`, items, next), response.body)
}

func startHistoryServer(t *testing.T, analyticsURL string) (*httptest.Server, http.Header, string) {
	t.Helper()
	config := application.Config{Relay: application.RelayConfig{
		AnalyticsURL:    analyticsURL,
		AnalyticsAPIKey: historyAnalyticsKey,
		HTTPTimeout:     time.Second,
		BatchSize:       10,
		PollInterval:    time.Second,
		Lease:           time.Minute,
		RetryBase:       time.Second,
		RetryMax:        time.Minute,
		Retention:       time.Hour,
	}}
	server := newAuthServer(t, testutil.Postgres(t), config)
	t.Cleanup(server.Close)
	owner := startHTTPSession(t, server)
	return server, bearer(owner.AccessToken), createEnrollmentMachine(t, server, owner.AccessToken)
}

func historyPath(machineID string) string {
	return machinesPath + "/" + machineID + "/telemetry"
}

func historyCursor(machineID string) string {
	return historyCursorAt(machineID, "2026-11-24T12:00:00.123456Z", "88888888-8888-4888-8888-888888888888")
}

func historyCursorAt(machineID, observedAt, eventID string) string {
	const cursor = `{"machine_id":%q,"observed_at":%q,"device_id":"77777777-7777-4777-8777-777777777777","event_id":%q}`
	return base64.RawURLEncoding.EncodeToString(fmt.Appendf(nil, cursor, machineID, observedAt, eventID))
}
