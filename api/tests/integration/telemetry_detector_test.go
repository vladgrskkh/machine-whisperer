//go:build integration

package integration_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type alertResponse struct {
	ID                      string     `json:"id"`
	MachineID               string     `json:"machine_id"`
	TriggerDeviceID         string     `json:"trigger_device_id"`
	TriggerEventID          string     `json:"trigger_event_id"`
	TriggeredAt             time.Time  `json:"triggered_at"`
	TriggerZScore           float64    `json:"trigger_z_score"`
	TriggerBaselineVersion  int64      `json:"trigger_baseline_version"`
	TriggerMeanRMSG         float64    `json:"trigger_mean_rms_g"`
	TriggerStddevRMSG       float64    `json:"trigger_stddev_rms_g"`
	LastZScore              float64    `json:"last_z_score"`
	PeakZScore              float64    `json:"peak_z_score"`
	RecoveryDeviceID        *string    `json:"recovery_device_id"`
	RecoveryEventID         *string    `json:"recovery_event_id"`
	RecoveredAt             *time.Time `json:"recovered_at"`
	RecoveryBaselineVersion *int64     `json:"recovery_baseline_version"`
	AcknowledgedAt          *time.Time `json:"acknowledged_at"`
}

type detectorFixture struct {
	telemetryFixture
}

func setupDetector(t *testing.T) detectorFixture {
	t.Helper()
	return detectorFixture{setupDeviceTelemetry(t)}
}

func (f detectorFixture) baseline(t *testing.T, stddev float64) machineBaselineResponse {
	t.Helper()
	return readMachineBaseline(t, authHTTPRequest(t, f.server, http.MethodPut,
		machinesPath+"/"+f.machineID+"/baseline", fmt.Sprintf(`{"mean_rms_g":10,"stddev_rms_g":%g}`, stddev),
		bearer(f.factory.AccessToken)))
}

func detectorObservation(eventID string, observedAt time.Time, state string, reading float64) string {
	return fmt.Sprintf(`{"schema_version":1,"event_id":%q,"observed_at":%q,"operating_state":%q,"vibration_rms_g":%g}`,
		eventID, observedAt.Format(time.RFC3339Nano), state, reading)
}

func (f detectorFixture) observe(t *testing.T, index int, state string, reading float64) string {
	t.Helper()
	eventID := uuid.NewString()
	body := detectorObservation(eventID, f.observed.Add(time.Duration(index)*time.Second), state, reading)
	response := f.post(t, body)
	require.Equal(t, http.StatusAccepted, response.status, response.body)
	return eventID
}

func (f detectorFixture) readings(t *testing.T, start int, readings ...float64) string {
	t.Helper()
	var eventID string
	for index, reading := range readings {
		eventID = f.observe(t, start+index, "running", reading)
	}
	return eventID
}

func (f detectorFixture) history(t *testing.T) []alertResponse {
	t.Helper()
	response := authHTTPRequest(t, f.server, http.MethodGet, machinesPath+"/"+f.machineID+"/alerts", "",
		bearer(f.factory.AccessToken))
	require.Equal(t, http.StatusOK, response.status, response.body)
	assert.Equal(t, "no-store", response.header.Get("Cache-Control"))
	var alerts []alertResponse
	require.NoError(t, json.Unmarshal([]byte(response.body), &alerts))
	require.NotNil(t, alerts)
	return alerts
}

func (f detectorFixture) latestAlert(t *testing.T) alertResponse {
	t.Helper()
	alerts := f.history(t)
	require.NotEmpty(t, alerts)
	return alerts[0]
}

func acknowledgePath(alertID string) string {
	return "/v1/alerts/" + alertID + "/acknowledge"
}

func (f detectorFixture) acknowledge(t *testing.T, alertID string) {
	t.Helper()
	response := authHTTPRequest(
		t,
		f.server,
		http.MethodPost,
		acknowledgePath(alertID),
		"",
		bearer(f.factory.AccessToken),
	)
	require.Equal(t, http.StatusNoContent, response.status, response.body)
	assert.Equal(t, "no-store", response.header.Get("Cache-Control"))
}

func TestTelemetryDetectorTransitions(t *testing.T) {
	t.Parallel()
	f := setupDetector(t)
	f.readings(t, 0, 100, 100, 100)
	assert.Empty(t, f.history(t))
	var baselineStateExists bool
	require.NoError(t, f.pool.QueryRow(t.Context(), `SELECT EXISTS (
		SELECT 1 FROM machine_detector_state WHERE machine_id=$1
	)`, f.machineID).Scan(&baselineStateExists))
	assert.False(t, baselineStateExists)

	f.baseline(t, 2)
	f.readings(t, 3, 18, 18)
	assert.Empty(t, f.history(t))
	f.readings(t, 5, 15, 18, 18)
	assert.Empty(t, f.history(t))
	trigger := f.readings(t, 8, 18)
	history := f.history(t)
	require.Len(t, history, 1)
	opened := history[0]
	assert.Equal(t, f.machineID, opened.MachineID)
	assert.Equal(t, f.device.DeviceID, opened.TriggerDeviceID)
	assert.Equal(t, trigger, opened.TriggerEventID)
	assert.Equal(t, f.observed.Add(8*time.Second), opened.TriggeredAt)
	assert.InDelta(t, 4.0, opened.TriggerZScore, 0)
	assert.EqualValues(t, 1, opened.TriggerBaselineVersion)
	assert.InDelta(t, 10.0, opened.TriggerMeanRMSG, 0)
	assert.InDelta(t, 2.0, opened.TriggerStddevRMSG, 0)
	assert.InDelta(t, 4.0, opened.LastZScore, 0)
	assert.InDelta(t, 4.0, opened.PeakZScore, 0)

	f.readings(t, 9, 22, 20, 12, 12)
	assert.Nil(t, f.latestAlert(t).RecoveredAt)
	f.baseline(t, 4)
	f.readings(t, 13, 12, 12)
	active := f.latestAlert(t)
	assert.Nil(t, active.RecoveredAt)
	assert.InDelta(t, 0.5, active.LastZScore, 0)
	assert.InDelta(t, 6.0, active.PeakZScore, 0)

	recovery := f.readings(t, 15, 12)
	recovered := f.latestAlert(t)
	require.NotNil(t, recovered.RecoveredAt)
	assert.Equal(t, f.observed.Add(15*time.Second), *recovered.RecoveredAt)
	require.NotNil(t, recovered.RecoveryEventID)
	assert.Equal(t, recovery, *recovered.RecoveryEventID)
	require.NotNil(t, recovered.RecoveryDeviceID)
	assert.Equal(t, f.device.DeviceID, *recovered.RecoveryDeviceID)
	require.NotNil(t, recovered.RecoveryBaselineVersion)
	assert.EqualValues(t, 2, *recovered.RecoveryBaselineVersion)
	assert.Equal(t, opened.TriggerEventID, recovered.TriggerEventID)
	assert.Equal(t, opened.TriggeredAt, recovered.TriggeredAt)
	assert.InDelta(t, opened.TriggerZScore, recovered.TriggerZScore, 0)
	assert.Equal(t, opened.TriggerBaselineVersion, recovered.TriggerBaselineVersion)
	assert.InDelta(t, opened.TriggerMeanRMSG, recovered.TriggerMeanRMSG, 0)
	assert.InDelta(t, opened.TriggerStddevRMSG, recovered.TriggerStddevRMSG, 0)

	f.readings(t, 16, 24, 24, 24)
	history = f.history(t)
	require.Len(t, history, 2)
	assert.Nil(t, history[0].RecoveredAt)
	assert.Equal(t, recovered, history[1])
}

func TestTelemetryDetectorIdempotentRetry(t *testing.T) {
	t.Parallel()
	f := setupDetector(t)
	f.baseline(t, 2)
	body := detectorObservation(uuid.NewString(), f.observed, "running", 18)
	for range 3 {
		require.Equal(t, http.StatusAccepted, f.post(t, body).status)
	}
	f.readings(t, 1, 18)
	assert.Empty(t, f.history(t))
	trigger := f.readings(t, 2, 18)
	require.Equal(t, http.StatusAccepted, f.post(t, body).status)
	history := f.history(t)
	require.Len(t, history, 1)
	assert.Equal(t, trigger, history[0].TriggerEventID)
}

func TestTelemetryDetectorRejectedObservationDoesNotAdvance(t *testing.T) {
	t.Parallel()
	f := setupDetector(t)
	f.baseline(t, 2)
	first := f.readings(t, 1, 18)
	require.Equal(t, http.StatusConflict,
		f.post(t, detectorObservation(uuid.NewString(), f.observed, "running", 18)).status)
	require.Equal(t, http.StatusConflict,
		f.post(t, detectorObservation(first, f.observed.Add(time.Second), "running", 19)).status)
	require.Equal(t, http.StatusConflict,
		f.post(t, detectorObservation(uuid.NewString(), f.observed.Add(6*time.Minute), "running", 18)).status)
	f.readings(t, 2, 18)
	assert.Empty(t, f.history(t))
	f.readings(t, 3, 18)
	require.Len(t, f.history(t), 1)
}

func TestTelemetryDetectorUnknownDoesNotResolve(t *testing.T) {
	t.Parallel()
	f := setupDetector(t)
	f.baseline(t, 2)
	f.readings(t, 0, 18, 18)
	f.observe(t, 2, "unknown", 30)
	f.readings(t, 3, 18, 18)
	assert.Empty(t, f.history(t))
	f.readings(t, 5, 18, 12, 12)
	f.observe(t, 8, "unknown", 30)
	active := f.latestAlert(t)
	assert.Nil(t, active.RecoveredAt)
	assert.InDelta(t, 1.0, active.LastZScore, 0)
	assert.InDelta(t, 4.0, active.PeakZScore, 0)
	f.readings(t, 9, 12, 12)
	assert.Nil(t, f.latestAlert(t).RecoveredAt)
	f.readings(t, 11, 12)
	assert.NotNil(t, f.latestAlert(t).RecoveredAt)
}

func TestTelemetryDetectorInclusiveThresholds(t *testing.T) {
	t.Parallel()
	f := setupDetector(t)
	f.baseline(t, 2)
	f.readings(t, 0, 4, 4, 4)
	history := f.history(t)
	require.Len(t, history, 1)
	assert.InDelta(t, 3.0, history[0].TriggerZScore, 0)
	f.readings(t, 3, 14, 14, 15, 14, 14)
	assert.Nil(t, f.latestAlert(t).RecoveredAt)
	f.readings(t, 8, 14)
	assert.NotNil(t, f.latestAlert(t).RecoveredAt)
	assert.InDelta(t, 2.0, f.latestAlert(t).LastZScore, 0)
}

func TestTelemetryDetectorIdenticalBaselinePreservesCounters(t *testing.T) {
	t.Parallel()
	f := setupDetector(t)
	initial := f.baseline(t, 2)
	f.readings(t, 0, 18, 18)
	assert.Equal(t, initial, f.baseline(t, 2))
	f.readings(t, 2, 18, 12, 12)
	assert.Equal(t, initial, f.baseline(t, 2))
	f.readings(t, 5, 12)
	history := f.history(t)
	require.Len(t, history, 1)
	assert.NotNil(t, history[0].RecoveredAt)
}

func TestBaselineUpdateSerializesWithTelemetry(t *testing.T) {
	t.Parallel()
	f := setupDetector(t)
	f.baseline(t, 2)
	f.readings(t, 0, 18, 18)
	ctx := t.Context()
	tx, err := f.pool.Begin(ctx)
	require.NoError(t, err)
	defer tx.Rollback(context.WithoutCancel(ctx))
	_, err = tx.Exec(ctx, `SELECT id FROM machines WHERE id=$1 FOR UPDATE`, f.machineID)
	require.NoError(t, err)
	_, err = tx.Exec(ctx, `UPDATE machine_baselines SET mean_rms_g=11, version=2 WHERE machine_id=$1`, f.machineID)
	require.NoError(t, err)

	const index = 2
	body := detectorObservation(uuid.NewString(), f.observed.Add(index*time.Second), "running", 18)
	type result struct {
		response authHTTPResponse
		err      error
	}
	done := make(chan result, 1)
	go func() {
		response, requestErr := sendAuthHTTPRequest(ctx, f.server.Client(), http.MethodPost,
			f.server.URL+telemetryPath(f.machineID), body, bearer(f.device.APIKey))
		done <- result{response, requestErr}
	}()
	blockerPID := int(tx.Conn().PgConn().PID())
	require.Eventually(t, func() bool { return telemetryLockHasWaiter(ctx, f.pool, blockerPID) },
		5*time.Second, 10*time.Millisecond)
	require.NoError(t, tx.Commit(ctx))
	item := <-done
	require.NoError(t, item.err)
	require.Equal(t, http.StatusAccepted, item.response.status, item.response.body)
	f.readings(t, 3, 18)
	assert.Empty(t, f.history(t))
	f.readings(t, 4, 18)
	history := f.history(t)
	require.Len(t, history, 1)
	assert.EqualValues(t, 2, history[0].TriggerBaselineVersion)
}

func TestConcurrentTelemetryDetectionOneAlert(t *testing.T) {
	t.Parallel()
	f := setupDetector(t)
	f.baseline(t, 2)
	f.readings(t, 0, 18, 18)
	const requests = 6
	start := make(chan struct{})
	var workers sync.WaitGroup
	for range requests {
		body := detectorObservation(uuid.NewString(), f.observed.Add(2*time.Second), "running", 18)
		workers.Go(func() {
			<-start
			response, err := sendAuthHTTPRequest(t.Context(), f.server.Client(), http.MethodPost,
				f.server.URL+telemetryPath(f.machineID), body, bearer(f.device.APIKey))
			assert.NoError(t, err)
			assert.Equal(t, http.StatusAccepted, response.status, response.body)
		})
	}
	close(start)
	workers.Wait()
	require.Len(t, f.history(t), 1)
	at := f.observed.Add(2 * time.Second)
	f.assertStored(t, requests+2, requests+2, &at)
}

func TestAlertAcknowledgementDoesNotResolve(t *testing.T) {
	t.Parallel()
	f := setupDetector(t)
	f.baseline(t, 2)
	f.readings(t, 0, 18, 18, 18)
	id := f.latestAlert(t).ID
	_, other := signupEnrollmentUser(t, f.server, `{"email":"other@example.com","password":"long enough password"}`)
	for _, endpoint := range []struct{ method, path, missingPath string }{
		{http.MethodGet, machinesPath + "/" + f.machineID + "/alerts", machinesPath + "/" + uuid.NewString() + "/alerts"},
		{http.MethodPost, acknowledgePath(id), acknowledgePath(uuid.NewString())},
	} {
		unauthenticated := authHTTPRequest(t, f.server, endpoint.method, endpoint.path, "", nil)
		assert.Equal(t, http.StatusUnauthorized, unauthenticated.status)
		forbidden := authHTTPRequest(t, f.server, endpoint.method, endpoint.path, "", bearer(other.AccessToken))
		missing := authHTTPRequest(t, f.server, endpoint.method, endpoint.missingPath, "", bearer(other.AccessToken))
		assert.Equal(t, http.StatusNotFound, forbidden.status)
		assert.Equal(t, http.StatusNotFound, missing.status)
		assert.Equal(t, missing.body, forbidden.body)
	}
	assert.Nil(t, f.latestAlert(t).AcknowledgedAt)

	f.acknowledge(t, id)
	acknowledged := f.latestAlert(t)
	require.NotNil(t, acknowledged.AcknowledgedAt)
	assert.Nil(t, acknowledged.RecoveredAt)
	f.readings(t, 3, 24)
	history := f.history(t)
	require.Len(t, history, 1)
	assert.Equal(t, id, history[0].ID)
	assert.InDelta(t, 7.0, history[0].LastZScore, 0)
	assert.Equal(t, acknowledged.AcknowledgedAt, history[0].AcknowledgedAt)
}

func TestRecoveredAlertCanBeAcknowledgedIdempotently(t *testing.T) {
	t.Parallel()
	f := setupDetector(t)
	f.baseline(t, 2)
	f.readings(t, 0, 18, 18, 18, 12, 12, 12)
	recovered := f.latestAlert(t)
	require.NotNil(t, recovered.RecoveredAt)
	f.acknowledge(t, recovered.ID)
	acknowledged := f.latestAlert(t)
	assert.Equal(t, recovered.RecoveredAt, acknowledged.RecoveredAt)
	require.NotNil(t, acknowledged.AcknowledgedAt)
	f.acknowledge(t, recovered.ID)
	assert.Equal(t, acknowledged, f.latestAlert(t))
}

func TestTelemetryDetectorTransactionRollback(t *testing.T) {
	t.Parallel()
	f := setupDetector(t)
	f.baseline(t, 2)
	f.readings(t, 0, 18, 18)
	_, err := f.pool.Exec(t.Context(), `CREATE FUNCTION reject_detector_alert() RETURNS trigger
		LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'forced test rollback'; END; $$;
		CREATE CONSTRAINT TRIGGER reject_detector_alert AFTER INSERT ON machine_alerts
		DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION reject_detector_alert();`)
	require.NoError(t, err)

	id := uuid.NewString()
	at := f.observed.Add(2 * time.Second)
	body := detectorObservation(id, at, "running", 18)
	response := f.post(t, body)
	require.Equal(t, http.StatusInternalServerError, response.status, response.body)
	assert.JSONEq(t, internalAuthErrorJSON, response.body)
	require.Empty(t, f.history(t))
	previous := f.observed.Add(time.Second)
	f.assertStored(t, 2, 2, &previous)
	var pending int
	require.NoError(t, f.pool.QueryRow(t.Context(), `SELECT pending_alert_count FROM machine_detector_state
		WHERE machine_id=$1`, f.machineID).Scan(&pending))
	assert.Equal(t, 2, pending)

	_, err = f.pool.Exec(t.Context(), `DROP TRIGGER reject_detector_alert ON machine_alerts`)
	require.NoError(t, err)
	require.Equal(t, http.StatusAccepted, f.post(t, body).status)
	f.assertStored(t, 3, 3, &at)
	history := f.history(t)
	require.Len(t, history, 1)
	assert.Equal(t, id, history[0].TriggerEventID)
}
