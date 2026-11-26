//go:build integration

package devicetelemetry_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/http/httptrace"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/trace/noop"

	"github.com/vladgrskkh/machine-whisperer/api/internal/application"
	"github.com/vladgrskkh/machine-whisperer/api/internal/domain/user"
	"github.com/vladgrskkh/machine-whisperer/api/internal/handler/middleware"
	devicepostgres "github.com/vladgrskkh/machine-whisperer/api/internal/infrastructure/device/postgres"
	"github.com/vladgrskkh/machine-whisperer/api/internal/usecase/device"
	"github.com/vladgrskkh/machine-whisperer/api/tests/integration/internal/testfixture"
)

func observationJSON(eventID string, observed time.Time) string {
	return fmt.Sprintf(
		`{"schema_version":1,"event_id":%q,"observed_at":%q,"operating_state":"running","vibration_rms_g":0.043}`,
		eventID,
		observed.Format(time.RFC3339Nano),
	)
}

func TestDeviceTelemetryRejectsInvalidCredentials(t *testing.T) {
	f := testfixture.SetupDeviceTelemetry(t)
	body := observationJSON(uuid.NewString(), f.Observed)
	otherKey := base64.RawURLEncoding.EncodeToString(make([]byte, 32))
	for name, headers := range map[string]http.Header{
		"missing":        nil,
		"two headers":    {"Authorization": {"Bearer " + f.Device.APIKey, "Bearer " + f.Device.APIKey}},
		"combined":       testfixture.Bearer(f.Device.APIKey + ",Bearer " + f.Device.APIKey),
		"wrong scheme":   {"Authorization": {"Basic " + f.Device.APIKey}},
		"extra space":    testfixture.Bearer(" " + f.Device.APIKey),
		"invalid UUID":   testfixture.Bearer("bad." + otherKey),
		"unknown device": testfixture.Bearer(uuid.NewString() + "." + otherKey),
		"wrong secret":   testfixture.Bearer(f.Device.DeviceID + "." + otherKey),
		"short secret":   testfixture.Bearer(f.Device.DeviceID + "." + otherKey[:42]),
		"noncanonical":   testfixture.Bearer(f.Device.DeviceID + "." + otherKey[:42] + "B"),
		"padded":         testfixture.Bearer(f.Device.APIKey + "="),
		"extra dot":      testfixture.Bearer(f.Device.APIKey + ".x"),
		"user JWT":       testfixture.Bearer(f.Factory.AccessToken),
	} {
		t.Run(name, func(t *testing.T) {
			response := testfixture.AuthHTTPRequest(
				t,
				f.Server,
				http.MethodPost,
				testfixture.TelemetryPath(f.MachineID),
				body,
				headers,
			)
			assert.Equal(t, http.StatusUnauthorized, response.Status)
			assert.Equal(t, "Bearer", response.Header.Get("WWW-Authenticate"))
			assert.Equal(t, "no-store", response.Header.Get("Cache-Control"))
			assert.JSONEq(t, testfixture.AuthFailedJSON, response.Body)
		})
	}
	f.AssertStored(t, 0, 0, nil)
}

func TestDeviceTelemetryRejectsUnclaimedRevokedAndWrongMachine(t *testing.T) {
	t.Parallel()
	f := testfixture.SetupDeviceTelemetry(t)
	unclaimed := testfixture.ProvisionEnrollment(t, f.Server, f.Factory.AccessToken)
	otherMachine := testfixture.CreateEnrollmentMachine(t, f.Server, f.Factory.AccessToken)
	body := observationJSON(uuid.NewString(), f.Observed)
	wrong := testfixture.AuthHTTPRequest(
		t,
		f.Server,
		http.MethodPost,
		testfixture.TelemetryPath(otherMachine),
		body,
		testfixture.Bearer(f.Device.APIKey),
	)
	assert.Equal(t, http.StatusNotFound, wrong.Status)

	response := testfixture.AuthHTTPRequest(
		t,
		f.Server,
		http.MethodPost,
		testfixture.TelemetryPath(f.MachineID),
		body,
		testfixture.Bearer(unclaimed.APIKey),
	)
	assert.Equal(t, http.StatusNotFound, response.Status)
	assert.JSONEq(t, wrong.Body, response.Body)

	_, err := f.Pool.Exec(t.Context(), "UPDATE devices SET revoked_at=clock_timestamp() WHERE id=$1", f.Device.DeviceID)
	require.NoError(t, err)
	revoked := f.Post(t, body)
	assert.Equal(t, http.StatusNotFound, revoked.Status)
	assert.JSONEq(t, wrong.Body, revoked.Body)
	badKey := testfixture.Bearer(f.Device.DeviceID + "." + base64.RawURLEncoding.EncodeToString(make([]byte, 32)))
	response = testfixture.AuthHTTPRequest(
		t,
		f.Server,
		http.MethodPost,
		testfixture.TelemetryPath(f.MachineID),
		body,
		badKey,
	)
	assert.Equal(t, http.StatusUnauthorized, response.Status)
	f.AssertStored(t, 0, 0, nil)
	metrics := testfixture.APIMetrics(t, testfixture.NewAPIMetricsServer(t, f.App))
	assert.Contains(t, metrics, "mw_telemetry_ingest_requests_total{outcome=\"machine_not_found\"} 1\n")
	assert.NotContains(t, metrics, "mw_telemetry_ingest_requests_total{outcome=\"unauthorized\"}")
	assert.Contains(t, metrics,
		`mw_http_requests_total{method="POST",route="/v1/machines/{machine_id}/telemetry",status_class="4xx"} 4`+"\n")
}

func TestDeviceTelemetryRejectsRevocationBetweenAuthenticationAndIngest(t *testing.T) {
	t.Parallel()
	f := testfixture.SetupDeviceTelemetry(t)
	ctx, cancel := context.WithTimeout(t.Context(), 8*time.Second)
	defer cancel()
	tx, err := f.Pool.Begin(ctx)
	require.NoError(t, err)
	defer func() { _ = tx.Rollback(t.Context()) }()

	var blockerPID int
	require.NoError(t, tx.QueryRow(ctx, "SELECT pg_backend_pid() FROM devices WHERE id=$1 FOR NO KEY UPDATE",
		f.Device.DeviceID).Scan(&blockerPID))

	// The server sends 100 Continue when the handler first reads the body, after device authentication.
	continued := make(chan struct{})
	requestCtx := httptrace.WithClientTrace(ctx, &httptrace.ClientTrace{
		Got100Continue: func() { close(continued) },
	})
	headers := testfixture.Bearer(f.Device.APIKey)
	headers.Set("Expect", "100-continue")
	body := observationJSON(uuid.NewString(), f.Observed)
	type result struct {
		response testfixture.AuthHTTPResponse
		err      error
	}
	results := make(chan result, 1)
	go func() {
		response, sendErr := testfixture.SendAuthHTTPRequest(requestCtx, f.Server.Client(), http.MethodPost,
			f.Server.URL+testfixture.TelemetryPath(f.MachineID), body, headers)
		results <- result{response: response, err: sendErr}
	}()

	select {
	case <-continued:
	case <-ctx.Done():
		t.Fatal("telemetry handler did not start reading the authenticated request body")
	}
	_, err = tx.Exec(ctx, "UPDATE devices SET revoked_at=clock_timestamp() WHERE id=$1", f.Device.DeviceID)
	require.NoError(t, err)

	// Keep the revocation uncommitted until ingestion waits for the lock or finishes without waiting.
	var completed bool
	var attempt result
	require.Eventually(t, func() bool {
		select {
		case attempt = <-results:
			completed = true
			return true
		default:
			return testfixture.TelemetryLockHasWaiter(ctx, f.Pool, blockerPID)
		}
	}, 5*time.Second, 10*time.Millisecond)
	require.NoError(t, tx.Commit(ctx))
	if !completed {
		select {
		case attempt = <-results:
		case <-ctx.Done():
			t.Fatal("telemetry handler did not respond after revocation committed")
		}
	}
	require.NoError(t, attempt.err)
	assert.Equal(t, http.StatusNotFound, attempt.response.Status)
	assert.Equal(t, "no-store", attempt.response.Header.Get("Cache-Control"))
	f.AssertStored(t, 0, 0, nil)
}

func TestDeviceKeyCannotAuthenticateUserRoutes(t *testing.T) {
	t.Parallel()
	f := testfixture.SetupDeviceTelemetry(t)
	for _, path := range []string{"/v1/users/me", testfixture.MachinesPath, testfixture.MachinesPath + "/" + f.MachineID} {
		response := testfixture.AuthHTTPRequest(
			t,
			f.Server,
			http.MethodGet,
			path,
			"",
			testfixture.Bearer(f.Device.APIKey),
		)
		assert.Equal(t, http.StatusUnauthorized, response.Status)
		assert.JSONEq(t, testfixture.AuthFailedJSON, response.Body)
	}
	assert.Equal(t, http.StatusAccepted, f.Post(t, observationJSON(uuid.NewString(), f.Observed)).Status)
}

func TestDeviceKeyRoleAuthorization(t *testing.T) {
	t.Parallel()
	f := testfixture.SetupDeviceTelemetry(t)
	authenticate := middleware.DeviceToken(
		device.NewAuthenticator(devicepostgres.New(f.Pool), noop.NewTracerProvider().Tracer("test")),
	)
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	routes := http.NewServeMux()
	routes.Handle("/telemetry", authenticate(middleware.RequireRole("telemetry_writer")(next)))
	routes.Handle("/provision", authenticate(middleware.RequireRole(user.RoleDeviceProvisioner)(next)))
	server := httptest.NewServer(routes)
	t.Cleanup(server.Close)

	headers := testfixture.Bearer(f.Device.APIKey)
	accepted := testfixture.AuthHTTPRequest(t, server, http.MethodPost, "/telemetry", "", headers)
	assert.Equal(t, http.StatusNoContent, accepted.Status)
	forbidden := testfixture.AuthHTTPRequest(t, server, http.MethodPost, "/provision", "", headers)
	assert.Equal(t, http.StatusForbidden, forbidden.Status)
}

func TestDeviceTelemetryValidatesV1Payload(t *testing.T) {
	f := testfixture.SetupDeviceTelemetry(t)
	eventID := uuid.NewString()
	body := observationJSON(eventID, f.Observed)
	for name, input := range map[string]string{
		"non-object":          `[]`,
		"malformed":           body[:len(body)-1],
		"trailing data":       body + " null",
		"too large":           body + strings.Repeat(" ", 8193-len(body)),
		"unknown key":         body[:len(body)-1] + `,"extra":true}`,
		"duplicate required":  body[:len(body)-1] + `,"event_id":"` + eventID + `"}`,
		"duplicate optional":  body[:len(body)-1] + `,"current_rms_a":null,"current_rms_a":null}`,
		"wrong version":       strings.Replace(body, `"schema_version":1`, `"schema_version":2`, 1),
		"invalid event ID":    strings.Replace(body, eventID, "not-a-uuid", 1),
		"invalid observation": strings.Replace(body, f.Observed.Format(time.RFC3339Nano), "not-a-timestamp", 1),
		"missing observation": strings.Replace(body, `"observed_at":"`+f.Observed.Format(time.RFC3339Nano)+`",`, "", 1),
		"idle":                strings.Replace(body, `"running"`, `"idle"`, 1),
		"negative RMS":        strings.Replace(body, "0.043", "-0.043", 1),
		"overflow RMS":        strings.Replace(body, "0.043", "1e999", 1),
		"null RMS":            strings.Replace(body, "0.043", "null", 1),
		"missing RMS":         strings.Replace(body, `,"vibration_rms_g":0.043`, "", 1),
		"negative frequency":  body[:len(body)-1] + `,"dominant_frequency_hz":-0.01}`,
		"overflow current":    body[:len(body)-1] + `,"current_rms_a":1e999}`,
		"string current":      body[:len(body)-1] + `,"current_rms_a":"3.2"}`,
	} {
		t.Run(name, func(t *testing.T) {
			response := f.Post(t, input)
			assert.Equal(t, http.StatusBadRequest, response.Status)
			assert.Equal(t, "no-store", response.Header.Get("Cache-Control"))
		})
	}
	badPath := testfixture.AuthHTTPRequest(
		t,
		f.Server,
		http.MethodPost,
		testfixture.TelemetryPath("not-a-uuid"),
		body,
		testfixture.Bearer(f.Device.APIKey),
	)
	assert.Equal(t, http.StatusBadRequest, badPath.Status)
	f.AssertStored(t, 0, 0, nil)

	stateAndValues := strings.Replace(body, `"running","vibration_rms_g":0.043}`,
		`"unknown","vibration_rms_g":0,"dominant_frequency_hz":0,"current_rms_a":3.2}`, 1)
	response := f.Post(t, stateAndValues+strings.Repeat(" ", 8192-len(stateAndValues)))
	assert.Equal(t, http.StatusAccepted, response.Status)
	assert.Equal(t, "no-store", response.Header.Get("Cache-Control"))
	f.AssertStored(t, 1, 1, &f.Observed)
	metrics := testfixture.APIMetrics(t, testfixture.NewAPIMetricsServer(t, f.App))
	assert.Contains(t, metrics, "mw_telemetry_ingest_requests_total{outcome=\"invalid\"} 5\n")
	assert.Contains(t, metrics,
		`mw_http_requests_total{method="POST",route="/v1/machines/{machine_id}/telemetry",status_class="4xx"} 20`+"\n")
	assert.Contains(t, metrics, "mw_detector_observations_total{outcome=\"no_baseline\"} 1\n")
	assert.Contains(t, metrics, "mw_detector_observations_total{outcome=\"unknown_state\"} 0\n")
}

func TestDeviceTelemetryIdenticalRetryIsIdempotent(t *testing.T) {
	t.Parallel()
	f := testfixture.SetupDeviceTelemetry(t)
	eventID := uuid.NewString()
	observed := f.Observed.Add(123456 * time.Microsecond)
	first := observationJSON(strings.ToUpper(eventID), observed.Add(100*time.Nanosecond))
	first = first[:len(first)-1] + `,"dominant_frequency_hz":-0.0}`
	require.Equal(t, http.StatusAccepted, f.Post(t, first).Status)

	latest := observed.Add(time.Microsecond)
	require.Equal(t, http.StatusAccepted, f.Post(t, observationJSON(uuid.NewString(), latest)).Status)
	offset := observed.Add(900 * time.Nanosecond).In(time.FixedZone("test offset", 7200))
	retry := fmt.Sprintf(`{"event_id":%q,"current_rms_a":null,"vibration_rms_g":43e-3,
		"operating_state":"running","observed_at":%q,"dominant_frequency_hz":0,"schema_version":1}`,
		eventID, offset.Format(time.RFC3339Nano))
	assert.Equal(t, http.StatusAccepted, f.Post(t, retry).Status)
	escaped := strings.Replace(observationJSON(eventID, observed), `Z"`, `\u005a"`, 1)
	escaped = strings.TrimSuffix(escaped, "}") + `,"dominant_frequency_hz":0}`
	assert.Equal(t, http.StatusAccepted, f.Post(t, escaped).Status)
	f.AssertStored(t, 2, 2, &latest)

	var payload string
	var receivedAt, expiresAt time.Time
	require.NoError(t, f.Pool.QueryRow(t.Context(), `SELECT payload::text,created_at,expires_at
		FROM telemetry_outbox JOIN telemetry_events USING(device_id,event_id)
		WHERE device_id=$1 AND event_id=$2`, f.Device.DeviceID, eventID).Scan(&payload, &receivedAt, &expiresAt))
	assert.JSONEq(t, fmt.Sprintf(`{"schema_version":1,"event_id":%q,"observed_at":%q,
		"operating_state":"running","vibration_rms_g":0.043,"dominant_frequency_hz":0,"current_rms_a":null}`,
		eventID, observed.Format(time.RFC3339Nano)), payload)
	assert.False(t, receivedAt.Before(f.Observed))
	assert.WithinDuration(t, observed.Add(30*24*time.Hour), expiresAt, 0)
	body := testfixture.APIMetrics(t, testfixture.NewAPIMetricsServer(t, f.App))
	assert.Contains(t, body, "mw_telemetry_ingest_requests_total{outcome=\"accepted_new\"} 2\n")
	assert.Contains(t, body, "mw_telemetry_ingest_requests_total{outcome=\"accepted_replay\"} 2\n")
	assert.Contains(t, body, "mw_detector_observations_total{outcome=\"no_baseline\"} 2\n")
}

func TestDeviceTelemetryConflictingEventIDReturnsConflict(t *testing.T) {
	t.Parallel()
	f := testfixture.SetupDeviceTelemetry(t)
	body := observationJSON(uuid.NewString(), f.Observed)
	require.Equal(t, http.StatusAccepted, f.Post(t, body).Status)
	response := f.Post(t, strings.Replace(body, "0.043", "0.044", 1))
	assert.Equal(t, http.StatusConflict, response.Status)
	f.AssertStored(t, 1, 1, &f.Observed)
	assert.Contains(t, testfixture.APIMetrics(t, testfixture.NewAPIMetricsServer(t, f.App)),
		"mw_telemetry_ingest_requests_total{outcome=\"conflict\"} 1\n")
}

func seedExpiredTelemetryBacklog(t *testing.T, f testfixture.TelemetryFixture, observedAt time.Time) {
	t.Helper()
	_, err := f.Pool.Exec(t.Context(), `INSERT INTO telemetry_events
		(device_id,event_id,machine_id,observed_at,payload_hash,expires_at)
		SELECT $1, gen_random_uuid(), $2, $3, $4, $5 FROM generate_series(1, 105)`,
		f.Device.DeviceID, f.MachineID, observedAt, make([]byte, 32), observedAt.Add(30*24*time.Hour))
	require.NoError(t, err)
}

func TestDeviceTelemetryCleanupIsBoundedAndMachineScoped(t *testing.T) {
	t.Parallel()
	f := testfixture.SetupDeviceTelemetry(t)
	old := f.Observed.Add(-30*24*time.Hour - time.Hour)
	seedExpiredTelemetryBacklog(t, f, old)

	otherMachine := testfixture.CreateEnrollmentMachine(t, f.Server, f.Factory.AccessToken)
	otherDevice := testfixture.ProvisionEnrollment(t, f.Server, f.Factory.AccessToken)
	claim := testfixture.AuthHTTPRequest(t, f.Server, http.MethodPost, testfixture.ClaimPath,
		testfixture.ClaimBody(otherDevice, otherMachine), testfixture.Bearer(f.Factory.AccessToken))
	require.Equal(t, http.StatusOK, claim.Status)
	_, err := f.Pool.Exec(t.Context(), testfixture.InsertTelemetryIdentity, otherDevice.DeviceID, uuid.NewString(),
		otherMachine, old, make([]byte, 32), old.Add(30*24*time.Hour))
	require.NoError(t, err)

	require.Equal(t, http.StatusAccepted, f.Post(t, observationJSON(uuid.NewString(), f.Observed)).Status)
	var ownExpired, otherExpired int
	require.NoError(t, f.Pool.QueryRow(t.Context(), `SELECT
		count(*) FILTER (WHERE machine_id=$1), count(*) FILTER (WHERE machine_id=$2)
		FROM telemetry_events WHERE expires_at <= clock_timestamp()`, f.MachineID, otherMachine).
		Scan(&ownExpired, &otherExpired))
	assert.Equal(t, 5, ownExpired)
	assert.Equal(t, 1, otherExpired)
}

func TestDeviceTelemetryReusesExpiredSubmittedIDOutsideCleanupBatch(t *testing.T) {
	t.Parallel()
	f := testfixture.SetupDeviceTelemetry(t)
	old := f.Observed.Add(-30*24*time.Hour - time.Hour)
	seedExpiredTelemetryBacklog(t, f, old)

	eventID := uuid.NewString()
	submittedOld := old.Add(time.Minute)
	_, err := f.Pool.Exec(t.Context(), testfixture.InsertTelemetryIdentity, f.Device.DeviceID, eventID,
		f.MachineID, submittedOld, make([]byte, 32), submittedOld.Add(30*24*time.Hour))
	require.NoError(t, err)

	require.Equal(t, http.StatusAccepted, f.Post(t, observationJSON(eventID, f.Observed)).Status)
	var renewedAt time.Time
	require.NoError(t, f.Pool.QueryRow(t.Context(), `SELECT expires_at FROM telemetry_events
		WHERE device_id=$1 AND event_id=$2`, f.Device.DeviceID, eventID).Scan(&renewedAt))
	assert.WithinDuration(t, f.Observed.Add(30*24*time.Hour), renewedAt, 0)
	f.AssertStored(t, 6, 1, &f.Observed)
}

func TestDeviceTelemetryOutboxKeyConflictReturnsConflict(t *testing.T) {
	t.Parallel()
	f := testfixture.SetupDeviceTelemetry(t)
	eventID := uuid.NewString()
	old := f.Observed.Add(-30*24*time.Hour - time.Minute)
	_, err := f.Pool.Exec(t.Context(), testfixture.InsertTelemetryIdentity, f.Device.DeviceID, eventID,
		f.MachineID, old, make([]byte, 32), old.Add(30*24*time.Hour))
	require.NoError(t, err)
	_, err = f.Pool.Exec(t.Context(), `INSERT INTO telemetry_outbox (device_id,event_id,machine_id,payload,created_at)
		VALUES ($1,$2,$3,'{}',$4)`, f.Device.DeviceID, eventID, f.MachineID, old)
	require.NoError(t, err)

	// A different accepted event expires the identity while leaving its original export queued.
	require.Equal(t, http.StatusAccepted, f.Post(t, observationJSON(uuid.NewString(), f.Observed)).Status)
	f.AssertStored(t, 1, 2, &f.Observed)
	response := f.Post(t, observationJSON(eventID, f.Observed.Add(time.Microsecond)))
	assert.Equal(t, http.StatusConflict, response.Status)
	f.AssertStored(t, 1, 2, &f.Observed)
}

func TestDeviceTelemetryRejectsFutureStaleAndOutOfOrderEvents(t *testing.T) {
	f := testfixture.SetupDeviceTelemetry(t)
	stale := f.Observed.Add(-30*24*time.Hour - time.Second)
	assert.Equal(t, http.StatusConflict, f.Post(t, observationJSON(uuid.NewString(), stale)).Status)
	f.AssertStored(t, 0, 0, nil)

	latest := f.Observed.Add(4 * time.Minute)
	_, err := f.Pool.Exec(
		t.Context(),
		`UPDATE machines SET last_telemetry_observed_at=$2 WHERE id=$1`,
		f.MachineID,
		latest,
	)
	require.NoError(t, err)
	for name, at := range map[string]time.Time{
		"future":       f.Observed.Add(6 * time.Minute),
		"out of order": latest.Add(-time.Microsecond),
	} {
		t.Run(name, func(t *testing.T) {
			assert.Equal(t, http.StatusConflict, f.Post(t, observationJSON(uuid.NewString(), at)).Status)
		})
	}
	f.AssertStored(t, 0, 0, &latest)
	assert.Equal(t, http.StatusAccepted, f.Post(t, observationJSON(uuid.NewString(), latest)).Status)
	f.AssertStored(t, 1, 1, &latest)
	body := testfixture.APIMetrics(t, testfixture.NewAPIMetricsServer(t, f.App))
	assert.Contains(t, body, "mw_telemetry_ingest_requests_total{outcome=\"outside_window\"} 2\n")
	assert.Contains(t, body, "mw_telemetry_ingest_requests_total{outcome=\"out_of_order\"} 1\n")
}

func TestDeviceTelemetryRejectsFutureUTCYearOverflow(t *testing.T) {
	t.Parallel()
	f := testfixture.SetupDeviceTelemetry(t)
	at, err := time.Parse(time.RFC3339, "9999-12-31T23:59:59-01:00")
	require.NoError(t, err)

	response := f.Post(t, observationJSON(uuid.NewString(), at))
	assert.Equal(t, http.StatusConflict, response.Status)
	f.AssertStored(t, 0, 0, nil)
}

func TestDeviceTelemetryConcurrentEventsPreserveOrder(t *testing.T) {
	t.Parallel()
	f := testfixture.SetupDeviceTelemetry(t)
	later := f.Observed.Add(time.Microsecond)
	olderBody := observationJSON(uuid.NewString(), f.Observed)
	laterBody := observationJSON(uuid.NewString(), later)
	bodies := []string{laterBody, olderBody, laterBody, olderBody}

	type result struct {
		body   string
		status int
		err    error
	}
	start := make(chan struct{})
	results := make(chan result, len(bodies))
	var workers sync.WaitGroup
	for _, body := range bodies {
		workers.Go(func() {
			<-start
			response, err := testfixture.SendAuthHTTPRequest(t.Context(), f.Server.Client(), http.MethodPost,
				f.Server.URL+testfixture.TelemetryPath(f.MachineID), body, testfixture.Bearer(f.Device.APIKey))
			results <- result{body: body, status: response.Status, err: err}
		})
	}
	close(start)
	workers.Wait()
	close(results)
	expectedCount := 1
	var olderStatuses []int
	for item := range results {
		require.NoError(t, item.err)
		if item.body == laterBody {
			assert.Equal(t, http.StatusAccepted, item.status)
		} else {
			olderStatuses = append(olderStatuses, item.status)
		}
	}
	require.Len(t, olderStatuses, 2)
	assert.Equal(t, olderStatuses[0], olderStatuses[1])
	assert.Contains(t, []int{http.StatusAccepted, http.StatusConflict}, olderStatuses[0])
	if olderStatuses[0] == http.StatusAccepted {
		expectedCount++
	}
	f.AssertStored(t, expectedCount, expectedCount, &later)
}

func TestDeviceTelemetryOutboxFailureRollsBack(t *testing.T) {
	t.Parallel()
	f := testfixture.SetupDeviceTelemetry(t)
	eventID := uuid.NewString()
	body := observationJSON(eventID, f.Observed)
	_, err := f.Pool.Exec(
		t.Context(),
		`ALTER TABLE telemetry_outbox ADD CONSTRAINT private_export_failure CHECK (false)`,
	)
	require.NoError(t, err)

	response := f.Post(t, body)
	assert.Equal(t, http.StatusInternalServerError, response.Status)
	assert.JSONEq(t, testfixture.InternalAuthErrorJSON, response.Body)
	f.AssertStored(t, 0, 0, nil)

	_, err = f.Pool.Exec(t.Context(), `ALTER TABLE telemetry_outbox DROP CONSTRAINT private_export_failure`)
	require.NoError(t, err)
	require.Equal(t, http.StatusAccepted, f.Post(t, body).Status)
	f.AssertStored(t, 1, 1, &f.Observed)
}

func TestDeviceTelemetryLogsExcludeSecretsAndBody(t *testing.T) {
	t.Parallel()
	var logs bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	f := testfixture.SetupDeviceTelemetry(t, application.WithLogger(logger))
	eventID := uuid.NewString()
	body := observationJSON(eventID, f.Observed)
	const queryValue = "private_query"
	path := testfixture.TelemetryPath(f.MachineID)
	response := testfixture.AuthHTTPRequest(
		t,
		f.Server,
		http.MethodPost,
		path+"?value="+queryValue,
		body,
		testfixture.Bearer(f.Device.APIKey),
	)
	assert.Equal(t, http.StatusAccepted, response.Status)

	const privateBody = `{"unrecognized":"private_body_canary"}`
	failure := testfixture.AuthHTTPRequest(t, f.Server, http.MethodPost, path, privateBody,
		testfixture.Bearer(f.Device.APIKey))
	assert.Equal(t, http.StatusBadRequest, failure.Status)
	f.Server.Close()

	entries := logs.String()
	assert.NotContains(t, entries, testfixture.EnrollmentKey(t, f.Device))
	assert.NotContains(t, entries, f.Device.ClaimPayload.ClaimCode)
	assert.NotContains(t, entries, queryValue)
	assert.NotContains(t, entries, eventID)
	assert.NotContains(t, entries, "vibration_rms_g")
	assert.NotContains(t, entries, "private_body_canary")
	var logged, loggedFailure bool
	for line := range strings.SplitSeq(strings.TrimSpace(entries), "\n") {
		var entry struct {
			Message  string `json:"msg"`
			Category string `json:"failure_category"`
			Status   int    `json:"http.response.status_code"`
		}
		require.NoError(t, json.Unmarshal([]byte(line), &entry))
		if entry.Message == "POST "+path {
			logged = true
		}
		if entry.Message == "telemetry ingest failed" {
			loggedFailure = true
			assert.Equal(t, "invalid_input", entry.Category)
			assert.Equal(t, http.StatusBadRequest, entry.Status)
		}
	}
	assert.True(t, logged)
	assert.True(t, loggedFailure, "rejected ingest should emit a safe business event")
}
