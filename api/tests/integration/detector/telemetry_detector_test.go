//go:build integration

package detector_test

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

	"github.com/vladgrskkh/machine-whisperer/api/internal/application"
	"github.com/vladgrskkh/machine-whisperer/api/tests/integration/internal/testfixture"
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
	testfixture.TelemetryFixture
}

func setupDetector(t *testing.T) detectorFixture {
	t.Helper()
	return detectorFixture{testfixture.SetupDeviceTelemetry(t)}
}

func (f detectorFixture) baseline(t *testing.T, stddev float64) testfixture.MachineBaselineResponse {
	t.Helper()
	return testfixture.ReadMachineBaseline(t, testfixture.AuthHTTPRequest(
		t,
		f.Server,
		http.MethodPut,
		testfixture.MachinesPath+"/"+f.MachineID+"/baseline",
		fmt.Sprintf(`{"mean_rms_g":10,"stddev_rms_g":%g}`, stddev),
		testfixture.Bearer(f.Factory.AccessToken),
	))
}

func detectorObservation(eventID string, observedAt time.Time, state string, reading float64) string {
	return fmt.Sprintf(`{"schema_version":1,"event_id":%q,"observed_at":%q,"operating_state":%q,"vibration_rms_g":%g}`,
		eventID, observedAt.Format(time.RFC3339Nano), state, reading)
}

func (f detectorFixture) observe(t *testing.T, index int, state string, reading float64) string {
	t.Helper()
	eventID := uuid.NewString()
	body := detectorObservation(eventID, f.Observed.Add(time.Duration(index)*time.Second), state, reading)
	response := f.Post(t, body)
	require.Equal(t, http.StatusAccepted, response.Status, response.Body)
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
	response := testfixture.AuthHTTPRequest(
		t,
		f.Server,
		http.MethodGet,
		testfixture.MachinesPath+"/"+f.MachineID+"/alerts",
		"",
		testfixture.Bearer(f.Factory.AccessToken),
	)
	require.Equal(t, http.StatusOK, response.Status, response.Body)
	assert.Equal(t, "no-store", response.Header.Get("Cache-Control"))
	var alerts []alertResponse
	require.NoError(t, json.Unmarshal([]byte(response.Body), &alerts))
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
	response := testfixture.AuthHTTPRequest(
		t,
		f.Server,
		http.MethodPost,
		acknowledgePath(alertID),
		"",
		testfixture.Bearer(f.Factory.AccessToken),
	)
	require.Equal(t, http.StatusNoContent, response.Status, response.Body)
	assert.Equal(t, "no-store", response.Header.Get("Cache-Control"))
}

func TestTelemetryDetectorTransitions(t *testing.T) {
	t.Parallel()
	f := setupDetector(t)
	f.readings(t, 0, 100, 100, 100)
	assert.Empty(t, f.history(t))
	var baselineStateExists bool
	require.NoError(t, f.Pool.QueryRow(t.Context(), `SELECT EXISTS (
		SELECT 1 FROM machine_detector_state WHERE machine_id=$1
	)`, f.MachineID).Scan(&baselineStateExists))
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
	assert.Equal(t, f.MachineID, opened.MachineID)
	assert.Equal(t, f.Device.DeviceID, opened.TriggerDeviceID)
	assert.Equal(t, trigger, opened.TriggerEventID)
	assert.Equal(t, f.Observed.Add(8*time.Second), opened.TriggeredAt)
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
	assert.Equal(t, f.Observed.Add(15*time.Second), *recovered.RecoveredAt)
	require.NotNil(t, recovered.RecoveryEventID)
	assert.Equal(t, recovery, *recovered.RecoveryEventID)
	require.NotNil(t, recovered.RecoveryDeviceID)
	assert.Equal(t, f.Device.DeviceID, *recovered.RecoveryDeviceID)
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
	body := detectorObservation(uuid.NewString(), f.Observed, "running", 18)
	for range 3 {
		require.Equal(t, http.StatusAccepted, f.Post(t, body).Status)
	}
	f.readings(t, 1, 18)
	assert.Empty(t, f.history(t))
	trigger := f.readings(t, 2, 18)
	require.Equal(t, http.StatusAccepted, f.Post(t, body).Status)
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
		f.Post(t, detectorObservation(uuid.NewString(), f.Observed, "running", 18)).Status)
	require.Equal(t, http.StatusConflict,
		f.Post(t, detectorObservation(first, f.Observed.Add(time.Second), "running", 19)).Status)
	require.Equal(t, http.StatusConflict,
		f.Post(t, detectorObservation(uuid.NewString(), f.Observed.Add(6*time.Minute), "running", 18)).Status)
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
	body := testfixture.APIMetrics(t, testfixture.NewAPIMetricsServer(t, f.App))
	assert.Contains(t, body, "mw_detector_observations_total{outcome=\"unknown_state\"} 2\n")
	assert.Contains(t, body, "mw_detector_observations_total{outcome=\"evaluated\"} 10\n")
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

func TestTelemetryDetectorZeroRecoveryThresholdRequiresExactMean(t *testing.T) {
	t.Parallel()
	config := testfixture.AuthConfig(t)
	config.Detector = application.DetectorConfig{
		AlertZThreshold: 3, RecoveryZThreshold: 0,
		AlertSustainedObservations: 3, RecoverySustainedObservations: 3,
	}
	f := detectorFixture{testfixture.SetupDeviceTelemetryWithConfig(t, config)}
	testfixture.ReadMachineBaseline(
		t,
		testfixture.AuthHTTPRequest(t, f.Server, http.MethodPut, testfixture.MachinesPath+"/"+f.MachineID+"/baseline",
			`{"mean_rms_g":0,"stddev_rms_g":1e300}`, testfixture.Bearer(f.Factory.AccessToken)),
	)

	f.readings(t, 0, 4e300, 4e300, 4e300)
	require.Nil(t, f.latestAlert(t).RecoveredAt)
	f.readings(t, 3, 0, 0)
	f.readings(t, 5, 1e-30, 1e-30, 1e-30)
	require.Nil(t, f.latestAlert(t).RecoveredAt, "nonzero deviation whose score underflows")

	f.readings(t, 8, 0, 0)
	require.Nil(t, f.latestAlert(t).RecoveredAt)
	recovery := f.readings(t, 10, 0)
	recovered := f.latestAlert(t)
	require.NotNil(t, recovered.RecoveredAt)
	require.NotNil(t, recovered.RecoveryEventID)
	assert.Equal(t, recovery, *recovered.RecoveryEventID)
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
	tx, err := f.Pool.Begin(ctx)
	require.NoError(t, err)
	defer tx.Rollback(context.WithoutCancel(ctx))
	_, err = tx.Exec(ctx, `SELECT id FROM machines WHERE id=$1 FOR UPDATE`, f.MachineID)
	require.NoError(t, err)
	_, err = tx.Exec(ctx, `UPDATE machine_baselines SET mean_rms_g=11, version=2 WHERE machine_id=$1`, f.MachineID)
	require.NoError(t, err)

	const index = 2
	body := detectorObservation(uuid.NewString(), f.Observed.Add(index*time.Second), "running", 18)
	type result struct {
		response testfixture.AuthHTTPResponse
		err      error
	}
	done := make(chan result, 1)
	go func() {
		response, requestErr := testfixture.SendAuthHTTPRequest(ctx, f.Server.Client(), http.MethodPost,
			f.Server.URL+testfixture.TelemetryPath(f.MachineID), body, testfixture.Bearer(f.Device.APIKey))
		done <- result{response, requestErr}
	}()
	blockerPID := int(tx.Conn().PgConn().PID())
	require.Eventually(t, func() bool { return testfixture.TelemetryLockHasWaiter(ctx, f.Pool, blockerPID) },
		5*time.Second, 10*time.Millisecond)
	require.NoError(t, tx.Commit(ctx))
	item := <-done
	require.NoError(t, item.err)
	require.Equal(t, http.StatusAccepted, item.response.Status, item.response.Body)
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
		body := detectorObservation(uuid.NewString(), f.Observed.Add(2*time.Second), "running", 18)
		workers.Go(func() {
			<-start
			response, err := testfixture.SendAuthHTTPRequest(t.Context(), f.Server.Client(), http.MethodPost,
				f.Server.URL+testfixture.TelemetryPath(f.MachineID), body, testfixture.Bearer(f.Device.APIKey))
			assert.NoError(t, err)
			assert.Equal(t, http.StatusAccepted, response.Status, response.Body)
		})
	}
	close(start)
	workers.Wait()
	require.Len(t, f.history(t), 1)
	at := f.Observed.Add(2 * time.Second)
	f.AssertStored(t, requests+2, requests+2, &at)
}

func TestAlertAcknowledgementDoesNotResolve(t *testing.T) {
	t.Parallel()
	f := setupDetector(t)
	f.baseline(t, 2)
	f.readings(t, 0, 18, 18, 18)
	id := f.latestAlert(t).ID
	_, other := testfixture.SignupEnrollmentUser(
		t,
		f.Server,
		`{"email":"other@example.com","password":"long enough password"}`,
	)
	for _, endpoint := range []struct{ method, path, missingPath string }{
		{http.MethodGet, testfixture.MachinesPath + "/" + f.MachineID + "/alerts", testfixture.MachinesPath + "/" + uuid.NewString() + "/alerts"},
		{http.MethodPost, acknowledgePath(id), acknowledgePath(uuid.NewString())},
	} {
		unauthenticated := testfixture.AuthHTTPRequest(t, f.Server, endpoint.method, endpoint.path, "", nil)
		assert.Equal(t, http.StatusUnauthorized, unauthenticated.Status)
		forbidden := testfixture.AuthHTTPRequest(
			t,
			f.Server,
			endpoint.method,
			endpoint.path,
			"",
			testfixture.Bearer(other.AccessToken),
		)
		missing := testfixture.AuthHTTPRequest(
			t,
			f.Server,
			endpoint.method,
			endpoint.missingPath,
			"",
			testfixture.Bearer(other.AccessToken),
		)
		assert.Equal(t, http.StatusNotFound, forbidden.Status)
		assert.Equal(t, http.StatusNotFound, missing.Status)
		assert.Equal(t, missing.Body, forbidden.Body)
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
	var acknowledgements sync.WaitGroup
	start := make(chan struct{})
	for range 8 {
		acknowledgements.Go(func() {
			<-start
			response := testfixture.AuthHTTPRequest(t, f.Server, http.MethodPost, acknowledgePath(recovered.ID),
				"", testfixture.Bearer(f.Factory.AccessToken))
			assert.Equal(t, http.StatusNoContent, response.Status)
		})
	}
	close(start)
	acknowledgements.Wait()
	acknowledged := f.latestAlert(t)
	assert.Equal(t, recovered.RecoveredAt, acknowledged.RecoveredAt)
	require.NotNil(t, acknowledged.AcknowledgedAt)
	f.acknowledge(t, recovered.ID)
	assert.Equal(t, acknowledged, f.latestAlert(t))
	body := testfixture.APIMetrics(t, testfixture.NewAPIMetricsServer(t, f.App))
	assert.Contains(t, body, "mw_alert_transitions_total{transition=\"opened\"} 1\n")
	assert.Contains(t, body, "mw_alert_transitions_total{transition=\"recovered\"} 1\n")
	assert.Contains(t, body, "mw_alert_transitions_total{transition=\"acknowledged\"} 1\n")
}

func TestTelemetryDetectorTransactionRollback(t *testing.T) {
	t.Parallel()
	f := setupDetector(t)
	f.baseline(t, 2)
	f.readings(t, 0, 18, 18)
	_, err := f.Pool.Exec(t.Context(), `CREATE FUNCTION reject_detector_alert() RETURNS trigger
		LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'forced test rollback'; END; $$;
		CREATE CONSTRAINT TRIGGER reject_detector_alert AFTER INSERT ON machine_alerts
		DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION reject_detector_alert();`)
	require.NoError(t, err)

	id := uuid.NewString()
	at := f.Observed.Add(2 * time.Second)
	body := detectorObservation(id, at, "running", 18)
	response := f.Post(t, body)
	require.Equal(t, http.StatusInternalServerError, response.Status, response.Body)
	assert.JSONEq(t, testfixture.InternalAuthErrorJSON, response.Body)
	require.Empty(t, f.history(t))
	previous := f.Observed.Add(time.Second)
	f.AssertStored(t, 2, 2, &previous)
	var pending int
	require.NoError(t, f.Pool.QueryRow(t.Context(), `SELECT pending_alert_count FROM machine_detector_state
		WHERE machine_id=$1`, f.MachineID).Scan(&pending))
	assert.Equal(t, 2, pending)
	metrics := testfixture.NewAPIMetricsServer(t, f.App)
	failed := testfixture.APIMetrics(t, metrics)
	assert.Contains(t, failed, "mw_telemetry_ingest_requests_total{outcome=\"accepted_new\"} 2\n")
	assert.Contains(t, failed, "mw_telemetry_ingest_requests_total{outcome=\"internal_error\"} 1\n")
	assert.Contains(t, failed, "mw_detector_observations_total{outcome=\"evaluated\"} 2\n")
	assert.Contains(t, failed, "mw_alert_transitions_total{transition=\"opened\"} 0\n")

	_, err = f.Pool.Exec(t.Context(), `DROP TRIGGER reject_detector_alert ON machine_alerts`)
	require.NoError(t, err)
	require.Equal(t, http.StatusAccepted, f.Post(t, body).Status)
	f.AssertStored(t, 3, 3, &at)
	history := f.history(t)
	require.Len(t, history, 1)
	assert.Equal(t, id, history[0].TriggerEventID)
	succeeded := testfixture.APIMetrics(t, metrics)
	assert.Contains(t, succeeded, "mw_telemetry_ingest_requests_total{outcome=\"accepted_new\"} 3\n")
	assert.Contains(t, succeeded, "mw_detector_observations_total{outcome=\"evaluated\"} 3\n")
	assert.Contains(t, succeeded, "mw_alert_transitions_total{transition=\"opened\"} 1\n")
}
