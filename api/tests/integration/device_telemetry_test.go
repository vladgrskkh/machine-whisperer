//go:build integration

package integration_test

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
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vladgrskkh/machine-whisperer/api/internal/application"
	"github.com/vladgrskkh/machine-whisperer/api/internal/domain/user"
	"github.com/vladgrskkh/machine-whisperer/api/internal/handler/middleware"
	devicepostgres "github.com/vladgrskkh/machine-whisperer/api/internal/infrastructure/device/postgres"
	"github.com/vladgrskkh/machine-whisperer/api/internal/testutil"
	"github.com/vladgrskkh/machine-whisperer/api/internal/usecase/device"
)

type telemetryFixture struct {
	pool      *pgxpool.Pool
	server    *httptest.Server
	factory   httpTokenPair
	device    factoryEnrollment
	machineID string
	observed  time.Time
}

func setupDeviceTelemetry(
	t *testing.T,
	postgresSuite *testutil.PostgresSuite,
	options ...application.Option,
) telemetryFixture {
	t.Helper()
	return setupDeviceTelemetryWithConfig(t, postgresSuite, authConfig(t), options...)
}

func setupDeviceTelemetryWithConfig(
	t *testing.T,
	postgresSuite *testutil.PostgresSuite,
	config application.Config,
	options ...application.Option,
) telemetryFixture {
	t.Helper()
	pool := postgresSuite.Pool(t)
	options = append([]application.Option{application.WithPool(pool)}, options...)
	app, err := application.New(t.Context(), config, options...)
	require.NoError(t, err)
	t.Cleanup(app.Close)
	server := httptest.NewServer(app.Routes())
	t.Cleanup(server.Close)
	factory := startFactorySession(t, pool, server)
	machineID := createEnrollmentMachine(t, server, factory.AccessToken)
	device := provisionEnrollment(t, server, factory.AccessToken)
	claim := authHTTPRequest(
		t,
		server,
		http.MethodPost,
		claimPath,
		claimBody(device, machineID),
		bearer(factory.AccessToken),
	)
	require.Equal(t, http.StatusOK, claim.status)
	var observed time.Time
	require.NoError(t, pool.QueryRow(t.Context(), "SELECT clock_timestamp()").Scan(&observed))
	return telemetryFixture{
		pool: pool, server: server, factory: factory,
		device: device, machineID: machineID, observed: observed.UTC().Truncate(time.Second),
	}
}

func telemetryPath(machineID string) string {
	return "/v1/machines/" + machineID + "/telemetry"
}

func observationJSON(eventID string, observed time.Time) string {
	return fmt.Sprintf(
		`{"schema_version":1,"event_id":%q,"observed_at":%q,"operating_state":"running","vibration_rms_g":0.043}`,
		eventID,
		observed.Format(time.RFC3339Nano),
	)
}

func (f telemetryFixture) post(t *testing.T, body string) authHTTPResponse {
	t.Helper()
	return authHTTPRequest(t, f.server, http.MethodPost, telemetryPath(f.machineID), body, bearer(f.device.APIKey))
}

func (f telemetryFixture) assertStored(t *testing.T, identities, outbox int, cursor *time.Time) {
	t.Helper()
	var storedIdentities, storedOutbox int
	var storedCursor *time.Time
	require.NoError(t, f.pool.QueryRow(t.Context(), `SELECT
		(SELECT count(*) FROM telemetry_events), (SELECT count(*) FROM telemetry_outbox),
		last_telemetry_observed_at FROM machines WHERE id=$1`, f.machineID).
		Scan(&storedIdentities, &storedOutbox, &storedCursor))
	assert.Equal(t, identities, storedIdentities)
	assert.Equal(t, outbox, storedOutbox)
	if cursor == nil {
		assert.Nil(t, storedCursor)
	} else {
		require.NotNil(t, storedCursor)
		assert.WithinDuration(t, *cursor, *storedCursor, 0)
	}
}

func TestDeviceTelemetryRejectsInvalidCredentials(t *testing.T) {
	f := setupDeviceTelemetry(t, &deviceTelemetryPostgres)
	body := observationJSON(uuid.NewString(), f.observed)
	otherKey := base64.RawURLEncoding.EncodeToString(make([]byte, 32))
	for name, headers := range map[string]http.Header{
		"missing":        nil,
		"two headers":    {"Authorization": {"Bearer " + f.device.APIKey, "Bearer " + f.device.APIKey}},
		"combined":       bearer(f.device.APIKey + ",Bearer " + f.device.APIKey),
		"wrong scheme":   {"Authorization": {"Basic " + f.device.APIKey}},
		"extra space":    bearer(" " + f.device.APIKey),
		"invalid UUID":   bearer("bad." + otherKey),
		"unknown device": bearer(uuid.NewString() + "." + otherKey),
		"wrong secret":   bearer(f.device.DeviceID + "." + otherKey),
		"short secret":   bearer(f.device.DeviceID + "." + otherKey[:42]),
		"noncanonical":   bearer(f.device.DeviceID + "." + otherKey[:42] + "B"),
		"padded":         bearer(f.device.APIKey + "="),
		"extra dot":      bearer(f.device.APIKey + ".x"),
		"user JWT":       bearer(f.factory.AccessToken),
	} {
		t.Run(name, func(t *testing.T) {
			response := authHTTPRequest(t, f.server, http.MethodPost, telemetryPath(f.machineID), body, headers)
			assert.Equal(t, http.StatusUnauthorized, response.status)
			assert.Equal(t, "Bearer", response.header.Get("WWW-Authenticate"))
			assert.Equal(t, "no-store", response.header.Get("Cache-Control"))
			assert.JSONEq(t, authFailedJSON, response.body)
		})
	}
	f.assertStored(t, 0, 0, nil)
}

func TestDeviceTelemetryRejectsUnclaimedRevokedAndWrongMachine(t *testing.T) {
	t.Parallel()
	f := setupDeviceTelemetry(t, &deviceTelemetryPostgres)
	unclaimed := provisionEnrollment(t, f.server, f.factory.AccessToken)
	otherMachine := createEnrollmentMachine(t, f.server, f.factory.AccessToken)
	body := observationJSON(uuid.NewString(), f.observed)
	wrong := authHTTPRequest(t, f.server, http.MethodPost, telemetryPath(otherMachine), body, bearer(f.device.APIKey))
	assert.Equal(t, http.StatusNotFound, wrong.status)

	response := authHTTPRequest(
		t,
		f.server,
		http.MethodPost,
		telemetryPath(f.machineID),
		body,
		bearer(unclaimed.APIKey),
	)
	assert.Equal(t, http.StatusNotFound, response.status)
	assert.JSONEq(t, wrong.body, response.body)

	_, err := f.pool.Exec(t.Context(), "UPDATE devices SET revoked_at=clock_timestamp() WHERE id=$1", f.device.DeviceID)
	require.NoError(t, err)
	revoked := f.post(t, body)
	assert.Equal(t, http.StatusNotFound, revoked.status)
	assert.JSONEq(t, wrong.body, revoked.body)
	badKey := bearer(f.device.DeviceID + "." + base64.RawURLEncoding.EncodeToString(make([]byte, 32)))
	response = authHTTPRequest(t, f.server, http.MethodPost, telemetryPath(f.machineID), body, badKey)
	assert.Equal(t, http.StatusUnauthorized, response.status)
	f.assertStored(t, 0, 0, nil)
}

func TestDeviceTelemetryRejectsRevocationBetweenAuthenticationAndIngest(t *testing.T) {
	t.Parallel()
	f := setupDeviceTelemetry(t, &deviceTelemetryPostgres)
	ctx, cancel := context.WithTimeout(t.Context(), 8*time.Second)
	defer cancel()
	tx, err := f.pool.Begin(ctx)
	require.NoError(t, err)
	defer func() { _ = tx.Rollback(t.Context()) }()

	var blockerPID int
	require.NoError(t, tx.QueryRow(ctx, "SELECT pg_backend_pid() FROM devices WHERE id=$1 FOR NO KEY UPDATE",
		f.device.DeviceID).Scan(&blockerPID))

	// The server sends 100 Continue when the handler first reads the body, after device authentication.
	continued := make(chan struct{})
	requestCtx := httptrace.WithClientTrace(ctx, &httptrace.ClientTrace{
		Got100Continue: func() { close(continued) },
	})
	headers := bearer(f.device.APIKey)
	headers.Set("Expect", "100-continue")
	body := observationJSON(uuid.NewString(), f.observed)
	type result struct {
		response authHTTPResponse
		err      error
	}
	results := make(chan result, 1)
	go func() {
		response, sendErr := sendAuthHTTPRequest(requestCtx, f.server.Client(), http.MethodPost,
			f.server.URL+telemetryPath(f.machineID), body, headers)
		results <- result{response: response, err: sendErr}
	}()

	select {
	case <-continued:
	case <-ctx.Done():
		t.Fatal("telemetry handler did not start reading the authenticated request body")
	}
	_, err = tx.Exec(ctx, "UPDATE devices SET revoked_at=clock_timestamp() WHERE id=$1", f.device.DeviceID)
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
			return telemetryLockHasWaiter(ctx, f.pool, blockerPID)
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
	assert.Equal(t, http.StatusNotFound, attempt.response.status)
	assert.Equal(t, "no-store", attempt.response.header.Get("Cache-Control"))
	f.assertStored(t, 0, 0, nil)
}

func TestDeviceKeyCannotAuthenticateUserRoutes(t *testing.T) {
	t.Parallel()
	f := setupDeviceTelemetry(t, &deviceTelemetryPostgres)
	for _, path := range []string{"/v1/users/me", machinesPath, machinesPath + "/" + f.machineID} {
		response := authHTTPRequest(t, f.server, http.MethodGet, path, "", bearer(f.device.APIKey))
		assert.Equal(t, http.StatusUnauthorized, response.status)
		assert.JSONEq(t, authFailedJSON, response.body)
	}
	assert.Equal(t, http.StatusAccepted, f.post(t, observationJSON(uuid.NewString(), f.observed)).status)
}

func TestDeviceKeyRoleAuthorization(t *testing.T) {
	t.Parallel()
	f := setupDeviceTelemetry(t, &deviceTelemetryPostgres)
	authenticate := middleware.DeviceToken(device.NewAuthenticator(devicepostgres.New(f.pool)))
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	routes := http.NewServeMux()
	routes.Handle("/telemetry", authenticate(middleware.RequireRole("telemetry_writer")(next)))
	routes.Handle("/provision", authenticate(middleware.RequireRole(user.RoleDeviceProvisioner)(next)))
	server := httptest.NewServer(routes)
	t.Cleanup(server.Close)

	headers := bearer(f.device.APIKey)
	accepted := authHTTPRequest(t, server, http.MethodPost, "/telemetry", "", headers)
	assert.Equal(t, http.StatusNoContent, accepted.status)
	forbidden := authHTTPRequest(t, server, http.MethodPost, "/provision", "", headers)
	assert.Equal(t, http.StatusForbidden, forbidden.status)
}

func TestDeviceTelemetryValidatesV1Payload(t *testing.T) {
	f := setupDeviceTelemetry(t, &deviceTelemetryPostgres)
	eventID := uuid.NewString()
	body := observationJSON(eventID, f.observed)
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
		"invalid observation": strings.Replace(body, f.observed.Format(time.RFC3339Nano), "not-a-timestamp", 1),
		"missing observation": strings.Replace(body, `"observed_at":"`+f.observed.Format(time.RFC3339Nano)+`",`, "", 1),
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
			response := f.post(t, input)
			assert.Equal(t, http.StatusBadRequest, response.status)
			assert.Equal(t, "no-store", response.header.Get("Cache-Control"))
		})
	}
	badPath := authHTTPRequest(t, f.server, http.MethodPost, telemetryPath("not-a-uuid"), body, bearer(f.device.APIKey))
	assert.Equal(t, http.StatusBadRequest, badPath.status)
	f.assertStored(t, 0, 0, nil)

	stateAndValues := strings.Replace(body, `"running","vibration_rms_g":0.043}`,
		`"unknown","vibration_rms_g":0,"dominant_frequency_hz":0,"current_rms_a":3.2}`, 1)
	response := f.post(t, stateAndValues+strings.Repeat(" ", 8192-len(stateAndValues)))
	assert.Equal(t, http.StatusAccepted, response.status)
	assert.Equal(t, "no-store", response.header.Get("Cache-Control"))
	f.assertStored(t, 1, 1, &f.observed)
}

func TestDeviceTelemetryIdenticalRetryIsIdempotent(t *testing.T) {
	t.Parallel()
	f := setupDeviceTelemetry(t, &deviceTelemetryPostgres)
	eventID := uuid.NewString()
	observed := f.observed.Add(123456 * time.Microsecond)
	first := observationJSON(strings.ToUpper(eventID), observed.Add(100*time.Nanosecond))
	first = first[:len(first)-1] + `,"dominant_frequency_hz":-0.0}`
	require.Equal(t, http.StatusAccepted, f.post(t, first).status)

	latest := observed.Add(time.Microsecond)
	require.Equal(t, http.StatusAccepted, f.post(t, observationJSON(uuid.NewString(), latest)).status)
	offset := observed.Add(900 * time.Nanosecond).In(time.FixedZone("test offset", 7200))
	retry := fmt.Sprintf(`{"event_id":%q,"current_rms_a":null,"vibration_rms_g":43e-3,
		"operating_state":"running","observed_at":%q,"dominant_frequency_hz":0,"schema_version":1}`,
		eventID, offset.Format(time.RFC3339Nano))
	assert.Equal(t, http.StatusAccepted, f.post(t, retry).status)
	escaped := strings.Replace(observationJSON(eventID, observed), `Z"`, `\u005a"`, 1)
	escaped = strings.TrimSuffix(escaped, "}") + `,"dominant_frequency_hz":0}`
	assert.Equal(t, http.StatusAccepted, f.post(t, escaped).status)
	f.assertStored(t, 2, 2, &latest)

	var payload string
	var receivedAt, expiresAt time.Time
	require.NoError(t, f.pool.QueryRow(t.Context(), `SELECT payload::text,created_at,expires_at
		FROM telemetry_outbox JOIN telemetry_events USING(device_id,event_id)
		WHERE device_id=$1 AND event_id=$2`, f.device.DeviceID, eventID).Scan(&payload, &receivedAt, &expiresAt))
	assert.JSONEq(t, fmt.Sprintf(`{"schema_version":1,"event_id":%q,"observed_at":%q,
		"operating_state":"running","vibration_rms_g":0.043,"dominant_frequency_hz":0,"current_rms_a":null}`,
		eventID, observed.Format(time.RFC3339Nano)), payload)
	assert.False(t, receivedAt.Before(f.observed))
	assert.WithinDuration(t, observed.Add(30*24*time.Hour), expiresAt, 0)
}

func TestDeviceTelemetryConflictingEventIDReturnsConflict(t *testing.T) {
	t.Parallel()
	f := setupDeviceTelemetry(t, &deviceTelemetryPostgres)
	body := observationJSON(uuid.NewString(), f.observed)
	require.Equal(t, http.StatusAccepted, f.post(t, body).status)
	response := f.post(t, strings.Replace(body, "0.043", "0.044", 1))
	assert.Equal(t, http.StatusConflict, response.status)
	f.assertStored(t, 1, 1, &f.observed)
}

func seedExpiredTelemetryBacklog(t *testing.T, f telemetryFixture, observedAt time.Time) {
	t.Helper()
	_, err := f.pool.Exec(t.Context(), `INSERT INTO telemetry_events
		(device_id,event_id,machine_id,observed_at,payload_hash,expires_at)
		SELECT $1, gen_random_uuid(), $2, $3, $4, $5 FROM generate_series(1, 105)`,
		f.device.DeviceID, f.machineID, observedAt, make([]byte, 32), observedAt.Add(30*24*time.Hour))
	require.NoError(t, err)
}

func TestDeviceTelemetryCleanupIsBoundedAndMachineScoped(t *testing.T) {
	t.Parallel()
	f := setupDeviceTelemetry(t, &deviceTelemetryPostgres)
	old := f.observed.Add(-30*24*time.Hour - time.Hour)
	seedExpiredTelemetryBacklog(t, f, old)

	otherMachine := createEnrollmentMachine(t, f.server, f.factory.AccessToken)
	otherDevice := provisionEnrollment(t, f.server, f.factory.AccessToken)
	claim := authHTTPRequest(t, f.server, http.MethodPost, claimPath,
		claimBody(otherDevice, otherMachine), bearer(f.factory.AccessToken))
	require.Equal(t, http.StatusOK, claim.status)
	_, err := f.pool.Exec(t.Context(), insertTelemetryIdentity, otherDevice.DeviceID, uuid.NewString(),
		otherMachine, old, make([]byte, 32), old.Add(30*24*time.Hour))
	require.NoError(t, err)

	require.Equal(t, http.StatusAccepted, f.post(t, observationJSON(uuid.NewString(), f.observed)).status)
	var ownExpired, otherExpired int
	require.NoError(t, f.pool.QueryRow(t.Context(), `SELECT
		count(*) FILTER (WHERE machine_id=$1), count(*) FILTER (WHERE machine_id=$2)
		FROM telemetry_events WHERE expires_at <= clock_timestamp()`, f.machineID, otherMachine).
		Scan(&ownExpired, &otherExpired))
	assert.Equal(t, 5, ownExpired)
	assert.Equal(t, 1, otherExpired)
}

func TestDeviceTelemetryReusesExpiredSubmittedIDOutsideCleanupBatch(t *testing.T) {
	t.Parallel()
	f := setupDeviceTelemetry(t, &deviceTelemetryPostgres)
	old := f.observed.Add(-30*24*time.Hour - time.Hour)
	seedExpiredTelemetryBacklog(t, f, old)

	eventID := uuid.NewString()
	submittedOld := old.Add(time.Minute)
	_, err := f.pool.Exec(t.Context(), insertTelemetryIdentity, f.device.DeviceID, eventID,
		f.machineID, submittedOld, make([]byte, 32), submittedOld.Add(30*24*time.Hour))
	require.NoError(t, err)

	require.Equal(t, http.StatusAccepted, f.post(t, observationJSON(eventID, f.observed)).status)
	var renewedAt time.Time
	require.NoError(t, f.pool.QueryRow(t.Context(), `SELECT expires_at FROM telemetry_events
		WHERE device_id=$1 AND event_id=$2`, f.device.DeviceID, eventID).Scan(&renewedAt))
	assert.WithinDuration(t, f.observed.Add(30*24*time.Hour), renewedAt, 0)
	f.assertStored(t, 6, 1, &f.observed)
}

func TestDeviceTelemetryOutboxKeyConflictReturnsConflict(t *testing.T) {
	t.Parallel()
	f := setupDeviceTelemetry(t, &deviceTelemetryPostgres)
	eventID := uuid.NewString()
	old := f.observed.Add(-30*24*time.Hour - time.Minute)
	_, err := f.pool.Exec(t.Context(), insertTelemetryIdentity, f.device.DeviceID, eventID,
		f.machineID, old, make([]byte, 32), old.Add(30*24*time.Hour))
	require.NoError(t, err)
	_, err = f.pool.Exec(t.Context(), `INSERT INTO telemetry_outbox (device_id,event_id,machine_id,payload,created_at)
		VALUES ($1,$2,$3,'{}',$4)`, f.device.DeviceID, eventID, f.machineID, old)
	require.NoError(t, err)

	// A different accepted event expires the identity while leaving its original export queued.
	require.Equal(t, http.StatusAccepted, f.post(t, observationJSON(uuid.NewString(), f.observed)).status)
	f.assertStored(t, 1, 2, &f.observed)
	response := f.post(t, observationJSON(eventID, f.observed.Add(time.Microsecond)))
	assert.Equal(t, http.StatusConflict, response.status)
	f.assertStored(t, 1, 2, &f.observed)
}

func TestDeviceTelemetryRejectsFutureStaleAndOutOfOrderEvents(t *testing.T) {
	f := setupDeviceTelemetry(t, &deviceTelemetryPostgres)
	stale := f.observed.Add(-30*24*time.Hour - time.Second)
	assert.Equal(t, http.StatusConflict, f.post(t, observationJSON(uuid.NewString(), stale)).status)
	f.assertStored(t, 0, 0, nil)

	latest := f.observed.Add(4 * time.Minute)
	_, err := f.pool.Exec(
		t.Context(),
		`UPDATE machines SET last_telemetry_observed_at=$2 WHERE id=$1`,
		f.machineID,
		latest,
	)
	require.NoError(t, err)
	for name, at := range map[string]time.Time{
		"future":       f.observed.Add(6 * time.Minute),
		"out of order": latest.Add(-time.Microsecond),
	} {
		t.Run(name, func(t *testing.T) {
			assert.Equal(t, http.StatusConflict, f.post(t, observationJSON(uuid.NewString(), at)).status)
		})
	}
	f.assertStored(t, 0, 0, &latest)
	assert.Equal(t, http.StatusAccepted, f.post(t, observationJSON(uuid.NewString(), latest)).status)
	f.assertStored(t, 1, 1, &latest)
}

func TestDeviceTelemetryRejectsFutureUTCYearOverflow(t *testing.T) {
	t.Parallel()
	f := setupDeviceTelemetry(t, &deviceTelemetryPostgres)
	at, err := time.Parse(time.RFC3339, "9999-12-31T23:59:59-01:00")
	require.NoError(t, err)

	response := f.post(t, observationJSON(uuid.NewString(), at))
	assert.Equal(t, http.StatusConflict, response.status)
	f.assertStored(t, 0, 0, nil)
}

func TestDeviceTelemetryConcurrentEventsPreserveOrder(t *testing.T) {
	t.Parallel()
	f := setupDeviceTelemetry(t, &deviceTelemetryPostgres)
	later := f.observed.Add(time.Microsecond)
	olderBody := observationJSON(uuid.NewString(), f.observed)
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
			response, err := sendAuthHTTPRequest(t.Context(), f.server.Client(), http.MethodPost,
				f.server.URL+telemetryPath(f.machineID), body, bearer(f.device.APIKey))
			results <- result{body: body, status: response.status, err: err}
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
	f.assertStored(t, expectedCount, expectedCount, &later)
}

func TestDeviceTelemetryOutboxFailureRollsBack(t *testing.T) {
	t.Parallel()
	f := setupDeviceTelemetry(t, &deviceTelemetryPostgres)
	eventID := uuid.NewString()
	body := observationJSON(eventID, f.observed)
	_, err := f.pool.Exec(
		t.Context(),
		`ALTER TABLE telemetry_outbox ADD CONSTRAINT private_export_failure CHECK (false)`,
	)
	require.NoError(t, err)

	response := f.post(t, body)
	assert.Equal(t, http.StatusInternalServerError, response.status)
	assert.JSONEq(t, internalAuthErrorJSON, response.body)
	f.assertStored(t, 0, 0, nil)

	_, err = f.pool.Exec(t.Context(), `ALTER TABLE telemetry_outbox DROP CONSTRAINT private_export_failure`)
	require.NoError(t, err)
	require.Equal(t, http.StatusAccepted, f.post(t, body).status)
	f.assertStored(t, 1, 1, &f.observed)
}

func TestDeviceTelemetryLogsExcludeSecretsAndBody(t *testing.T) {
	t.Parallel()
	var logs bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	f := setupDeviceTelemetry(t, &deviceTelemetryPostgres, application.WithLogger(logger))
	eventID := uuid.NewString()
	body := observationJSON(eventID, f.observed)
	const queryValue = "private_query"
	path := telemetryPath(f.machineID)
	response := authHTTPRequest(t, f.server, http.MethodPost, path+"?value="+queryValue, body, bearer(f.device.APIKey))
	assert.Equal(t, http.StatusAccepted, response.status)
	f.server.Close()

	entries := logs.String()
	assert.NotContains(t, entries, enrollmentKey(t, f.device))
	assert.NotContains(t, entries, f.device.ClaimPayload.ClaimCode)
	assert.NotContains(t, entries, queryValue)
	assert.NotContains(t, entries, eventID)
	assert.NotContains(t, entries, "vibration_rms_g")
	var logged bool
	for line := range strings.SplitSeq(strings.TrimSpace(entries), "\n") {
		var entry struct {
			Message string `json:"msg"`
		}
		require.NoError(t, json.Unmarshal([]byte(line), &entry))
		if entry.Message == "POST "+path {
			logged = true
		}
	}
	assert.True(t, logged)
}
