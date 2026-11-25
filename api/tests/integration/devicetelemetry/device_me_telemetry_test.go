//go:build integration

package devicetelemetry_test

import (
	"encoding/base64"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vladgrskkh/machine-whisperer/api/tests/integration/internal/testfixture"
)

const deviceMeTelemetryPath = "/v1/devices/me/telemetry"

func TestDeviceMeTelemetryRequiresAClaimedActiveDevice(t *testing.T) {
	t.Parallel()
	f := testfixture.SetupDeviceTelemetry(t)
	body := observationJSON(uuid.NewString(), f.Observed)
	wrongSecret := base64.RawURLEncoding.EncodeToString(make([]byte, 32))
	for name, headers := range map[string]http.Header{
		"missing credentials": nil,
		"wrong secret":        testfixture.Bearer(f.Device.DeviceID + "." + wrongSecret),
		"user JWT":            testfixture.Bearer(f.Factory.AccessToken),
	} {
		response := testfixture.AuthHTTPRequest(t, f.Server, http.MethodPost, deviceMeTelemetryPath, body, headers)
		assert.Equal(t, http.StatusUnauthorized, response.Status, name)
		assert.Equal(t, "Bearer", response.Header.Get("WWW-Authenticate"), name)
		assert.Equal(t, "no-store", response.Header.Get("Cache-Control"), name)
		assert.JSONEq(t, testfixture.AuthFailedJSON, response.Body, name)
	}

	unclaimed := testfixture.ProvisionEnrollment(t, f.Server, f.Factory.AccessToken)
	_, err := f.Pool.Exec(t.Context(), "UPDATE devices SET revoked_at=clock_timestamp() WHERE id=$1", f.Device.DeviceID)
	require.NoError(t, err)
	for name, key := range map[string]string{"unclaimed device": unclaimed.APIKey, "revoked device": f.Device.APIKey} {
		response := testfixture.AuthHTTPRequest(
			t,
			f.Server,
			http.MethodPost,
			deviceMeTelemetryPath,
			body,
			testfixture.Bearer(key),
		)
		assert.Equal(t, http.StatusNotFound, response.Status, name)
		assert.Equal(t, "no-store", response.Header.Get("Cache-Control"), name)
		assert.JSONEq(t, `{"error":{"code":"machine_not_found","message":"Machine not found"}}`, response.Body, name)
	}
	f.AssertStored(t, 0, 0, nil)
}

func TestDeviceMeTelemetryRequiresStrictObservationBody(t *testing.T) {
	t.Parallel()
	f := testfixture.SetupDeviceTelemetry(t)
	eventID := uuid.NewString()
	body := observationJSON(eventID, f.Observed)
	otherMachine := testfixture.CreateEnrollmentMachine(t, f.Server, f.Factory.AccessToken)
	headers := testfixture.Bearer(f.Device.APIKey)
	for name, input := range map[string]string{
		"machine ID":    fmt.Sprintf(`%s,"machine_id":%q}`, strings.TrimSuffix(body, "}"), otherMachine),
		"duplicate key": fmt.Sprintf(`%s,"event_id":%q}`, strings.TrimSuffix(body, "}"), eventID),
		"trailing JSON": body + " null",
	} {
		response := testfixture.AuthHTTPRequest(t, f.Server, http.MethodPost, deviceMeTelemetryPath, input, headers)
		assert.Equal(t, http.StatusBadRequest, response.Status, name)
		assert.Equal(t, "no-store", response.Header.Get("Cache-Control"), name)
	}
	f.AssertStored(t, 0, 0, nil)
}

func TestDeviceMeTelemetryPersistsToBoundMachineOnceAcrossRoutes(t *testing.T) {
	t.Parallel()
	f := testfixture.SetupDeviceTelemetry(t)
	eventID := uuid.NewString()
	body := observationJSON(eventID, f.Observed)
	headers := testfixture.Bearer(f.Device.APIKey)
	missingMachine := testfixture.AuthHTTPRequest(
		t,
		f.Server,
		http.MethodPost,
		testfixture.TelemetryPath(""),
		body,
		headers,
	)
	assert.Equal(t, http.StatusBadRequest, missingMachine.Status)
	response := testfixture.AuthHTTPRequest(t, f.Server, http.MethodPost, deviceMeTelemetryPath, body, headers)
	require.Equal(t, http.StatusAccepted, response.Status)
	assert.Empty(t, response.Body)
	assert.Equal(t, "no-store", response.Header.Get("Cache-Control"))
	assert.Equal(t, http.StatusAccepted, f.Post(t, body).Status)
	conflict := f.Post(t, strings.Replace(body, "0.043", "0.044", 1))
	assert.Equal(t, http.StatusConflict, conflict.Status)
	f.AssertStored(t, 1, 1, &f.Observed)

	var eventMachine, outboxMachine string
	require.NoError(t, f.Pool.QueryRow(t.Context(), `SELECT e.machine_id, o.machine_id
		FROM telemetry_events e JOIN telemetry_outbox o USING (device_id,event_id)
		WHERE e.device_id=$1 AND e.event_id=$2`, f.Device.DeviceID, eventID).Scan(&eventMachine, &outboxMachine))
	assert.Equal(t, f.MachineID, eventMachine)
	assert.Equal(t, f.MachineID, outboxMachine)
}
