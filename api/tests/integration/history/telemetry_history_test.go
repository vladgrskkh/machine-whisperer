//go:build integration

package history_test

import (
	"encoding/base64"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vladgrskkh/machine-whisperer/api/internal/application"
	"github.com/vladgrskkh/machine-whisperer/api/internal/testutil"
	"github.com/vladgrskkh/machine-whisperer/api/tests/integration/internal/testfixture"
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
	_, other := testfixture.SignupEnrollmentUser(
		t,
		server,
		`{"email":"other@example.com","password":"long enough password"}`,
	)

	path := historyPath(machineID)
	unauthenticated := testfixture.AuthHTTPRequest(t, server, http.MethodGet, path, "", nil)
	assert.Equal(t, http.StatusUnauthorized, unauthenticated.Status)
	foreign := testfixture.AuthHTTPRequest(t, server, http.MethodGet, path, "", testfixture.Bearer(other.AccessToken))
	missing := testfixture.AuthHTTPRequest(t, server, http.MethodGet, historyPath(uuid.NewString()), "", owner)
	assert.Equal(t, http.StatusNotFound, foreign.Status)
	assert.Equal(t, http.StatusNotFound, missing.Status)
	assert.JSONEq(t, missing.Body, foreign.Body)
	assert.Zero(t, calls.Load())

	response := testfixture.AuthHTTPRequest(t, server, http.MethodGet, path, "", owner)
	require.Equal(t, http.StatusOK, response.Status, response.Body)
	assert.Equal(t, "no-store", response.Header.Get("Cache-Control"))
	assert.Equal(t, "application/json", response.Header.Get("Content-Type"))
	assert.JSONEq(t, emptyHistoryPage, response.Body)
	assert.EqualValues(t, 1, calls.Load())

	machinePath := testfixture.MachinesPath + "/" + machineID
	deleted := testfixture.AuthHTTPRequest(t, server, http.MethodDelete, machinePath, "", owner)
	require.Equal(t, http.StatusNoContent, deleted.Status, deleted.Body)
	archived := testfixture.AuthHTTPRequest(t, server, http.MethodGet, path, "", owner)
	assert.Equal(t, http.StatusNotFound, archived.Status)
	assert.JSONEq(t, missing.Body, archived.Body)
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
	const eventID = "88888888-8888-4888-8888-888888888888"
	const observedAt = "2026-11-24T12:00:00.123456Z"
	cursorBody := strings.TrimSuffix(historyCursorJSON(machineID, observedAt, eventID), "}")

	for _, target := range []string{
		historyPath("not-a-uuid"),
		path + "?limit=0", path + "?limit=-1", path + "?limit=1001", path + "?limit=not-a-number",
		path + "?limit=999999999999999999999", path + "?limit=", path + "?limit=1&limit=2",
		path + "?limit=%", path + "?cursor=", path + "?cursor=not%2Fbase64",
		path + "?cursor=e30", path + "?cursor=e30&cursor=e30",
		path + "?cursor=" + historyCursor(uuid.NewString()),
		path + "?cursor=" + historyCursorAt(machineID, "1899-12-31T23:59:59.999999Z", eventID),
		path + "?cursor=" + historyCursorAt(machineID, "2263-01-01T00:00:00Z", eventID),
		path + "?cursor=" + historyCursorAt(machineID, observedAt, "ABCDEFAB-8888-4888-8888-888888888888"),
		path + "?cursor=" + base64.RawURLEncoding.EncodeToString([]byte(cursorBody+`,"event_id":"`+eventID+`"}`)),
		path + "?cursor=" + base64.RawURLEncoding.EncodeToString([]byte(cursorBody+`,"unexpected":true}`)),
		path + "?cursor=" + strings.Repeat("A", 513),
	} {
		response := testfixture.AuthHTTPRequest(t, server, http.MethodGet, target, "", owner)
		assert.Equal(t, http.StatusBadRequest, response.Status, target)
		assert.Contains(t, response.Body, `"invalid_input"`, target)
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
		response := testfixture.AuthHTTPRequest(t, server, http.MethodGet, path+target, "", owner)
		assert.NotContains(t, response.Body, sensitive)
		assert.NotContains(t, response.Body, analytics.URL)
		assert.NotContains(t, response.Body, historyAnalyticsKey)
		if target == "?limit=1" {
			assert.Equal(t, http.StatusBadRequest, response.Status)
			assert.Contains(t, response.Body, `"invalid_input"`)
		} else {
			assert.Equal(t, http.StatusServiceUnavailable, response.Status)
			assert.Contains(t, response.Body, `"telemetry_unavailable"`)
		}
	}
	assert.Zero(t, redirects.Load())
}

func TestTelemetryHistoryRejectsMalformedAnalyticsPage(t *testing.T) {
	t.Parallel()
	const valid = `{"device_id":"77777777-7777-4777-8777-777777777777",
		"event_id":"88888888-8888-4888-8888-888888888888","received_at":"2026-11-24T12:01:00.987654Z",
		"schema_version":1,"observed_at":"2026-11-24T12:00:00.123456Z","operating_state":"running",
		"vibration_rms_g":0,"dominant_frequency_hz":null,"current_rms_a":null}`
	cases := []struct {
		name   string
		items  string
		status int
	}{
		{"null_item", "null", http.StatusServiceUnavailable},
		{"empty_item", "{}", http.StatusServiceUnavailable},
		{"missing_rms", strings.Replace(valid, `"vibration_rms_g":0,`, "", 1), http.StatusServiceUnavailable},
		{"null_rms", strings.Replace(valid, `"vibration_rms_g":0`, `"vibration_rms_g":null`, 1),
			http.StatusServiceUnavailable},
		{"invalid_id", strings.Replace(valid, "77777777-7777-4777-8777-777777777777", "private invalid ID", 1),
			http.StatusServiceUnavailable},
		{"invalid_version", strings.Replace(valid, `"schema_version":1`, `"schema_version":2`, 1),
			http.StatusServiceUnavailable},
		{"invalid_state", strings.Replace(valid, `"operating_state":"running"`, `"operating_state":"idle"`, 1),
			http.StatusServiceUnavailable},
		{"null_timestamp", strings.Replace(valid, `"2026-11-24T12:00:00.123456Z"`, "null", 1),
			http.StatusServiceUnavailable},
		{"zero_rms_null_optional_readings", valid, http.StatusOK},
	}
	responses := make(map[string]string, len(cases))
	for index, tc := range cases {
		responses[strconv.Itoa(index+1)] = fmt.Sprintf(`{"items":[%s],"next_cursor":null}`, tc.items)
	}
	analytics := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, responses[r.URL.Query().Get("limit")])
	}))
	t.Cleanup(analytics.Close)
	server, owner, machineID := startHistoryServer(t, analytics.URL)
	for index, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			limit := strconv.Itoa(index + 1)
			response := testfixture.AuthHTTPRequest(
				t,
				server,
				http.MethodGet,
				historyPath(machineID)+"?limit="+limit,
				"",
				owner,
			)
			assert.Equal(t, tc.status, response.Status)
			if tc.status == http.StatusOK {
				assert.JSONEq(t, responses[limit], response.Body)
				return
			}
			assert.Contains(t, response.Body, `"telemetry_unavailable"`)
			assert.NotContains(t, response.Body, "private invalid ID")
		})
	}
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
	response := testfixture.AuthHTTPRequest(
		t, server, http.MethodGet, historyPath(machineID)+"?limit=1000&cursor="+cursor, "", owner,
	)
	require.Equal(t, http.StatusOK, response.Status, response.Body)
	assert.Equal(t, "/internal/v1/machines/"+machineID+"/telemetry", receivedPath.Load())
	assert.Equal(t, cursor, receivedCursor.Load())
	assert.JSONEq(t, fmt.Sprintf(`{"items":%s,"next_cursor":%q}`, items, next), response.Body)
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
	server := testfixture.NewAuthServer(t, testutil.Postgres(t), config)
	t.Cleanup(server.Close)
	owner := testfixture.StartHTTPSession(t, server)
	return server, testfixture.Bearer(
			owner.AccessToken,
		), testfixture.CreateEnrollmentMachine(
			t,
			server,
			owner.AccessToken,
		)
}

func historyPath(machineID string) string {
	return testfixture.MachinesPath + "/" + machineID + "/telemetry"
}

func historyCursor(machineID string) string {
	return historyCursorAt(machineID, "2026-11-24T12:00:00.123456Z", "88888888-8888-4888-8888-888888888888")
}

func historyCursorAt(machineID, observedAt, eventID string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(historyCursorJSON(machineID, observedAt, eventID)))
}

func historyCursorJSON(machineID, observedAt, eventID string) string {
	const cursor = `{"machine_id":%q,"observed_at":%q,"device_id":"77777777-7777-4777-8777-777777777777","event_id":%q}`
	return fmt.Sprintf(cursor, machineID, observedAt, eventID)
}
