//go:build integration

package integration_test

import (
	"encoding/base64"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const deviceMeTelemetryPath = "/v1/devices/me/telemetry"

func TestDeviceMeTelemetryRequiresAClaimedActiveDevice(t *testing.T) {
	t.Parallel()
	f := setupDeviceTelemetry(t)
	body := observationJSON(uuid.NewString(), f.observed)
	wrongSecret := base64.RawURLEncoding.EncodeToString(make([]byte, 32))
	for name, headers := range map[string]http.Header{
		"missing credentials": nil,
		"wrong secret":        bearer(f.device.DeviceID + "." + wrongSecret),
		"user JWT":            bearer(f.factory.AccessToken),
	} {
		response := authHTTPRequest(t, f.server, http.MethodPost, deviceMeTelemetryPath, body, headers)
		assert.Equal(t, http.StatusUnauthorized, response.status, name)
		assert.Equal(t, "Bearer", response.header.Get("WWW-Authenticate"), name)
		assert.Equal(t, "no-store", response.header.Get("Cache-Control"), name)
		assert.JSONEq(t, authFailedJSON, response.body, name)
	}

	unclaimed := provisionEnrollment(t, f.server, f.factory.AccessToken)
	_, err := f.pool.Exec(t.Context(), "UPDATE devices SET revoked_at=clock_timestamp() WHERE id=$1", f.device.DeviceID)
	require.NoError(t, err)
	for name, key := range map[string]string{"unclaimed device": unclaimed.APIKey, "revoked device": f.device.APIKey} {
		response := authHTTPRequest(t, f.server, http.MethodPost, deviceMeTelemetryPath, body, bearer(key))
		assert.Equal(t, http.StatusNotFound, response.status, name)
		assert.Equal(t, "no-store", response.header.Get("Cache-Control"), name)
		assert.JSONEq(t, `{"error":{"code":"machine_not_found","message":"Machine not found"}}`, response.body, name)
	}
	f.assertStored(t, 0, 0, nil)
}

func TestDeviceMeTelemetryRequiresStrictObservationBody(t *testing.T) {
	t.Parallel()
	f := setupDeviceTelemetry(t)
	eventID := uuid.NewString()
	body := observationJSON(eventID, f.observed)
	otherMachine := createEnrollmentMachine(t, f.server, f.factory.AccessToken)
	headers := bearer(f.device.APIKey)
	for name, input := range map[string]string{
		"machine ID":    fmt.Sprintf(`%s,"machine_id":%q}`, strings.TrimSuffix(body, "}"), otherMachine),
		"duplicate key": fmt.Sprintf(`%s,"event_id":%q}`, strings.TrimSuffix(body, "}"), eventID),
		"trailing JSON": body + " null",
	} {
		response := authHTTPRequest(t, f.server, http.MethodPost, deviceMeTelemetryPath, input, headers)
		assert.Equal(t, http.StatusBadRequest, response.status, name)
		assert.Equal(t, "no-store", response.header.Get("Cache-Control"), name)
	}
	f.assertStored(t, 0, 0, nil)
}

func TestDeviceMeTelemetryPersistsToBoundMachineOnceAcrossRoutes(t *testing.T) {
	t.Parallel()
	f := setupDeviceTelemetry(t)
	eventID := uuid.NewString()
	body := observationJSON(eventID, f.observed)
	headers := bearer(f.device.APIKey)
	missingMachine := authHTTPRequest(t, f.server, http.MethodPost, telemetryPath(""), body, headers)
	assert.Equal(t, http.StatusBadRequest, missingMachine.status)
	response := authHTTPRequest(t, f.server, http.MethodPost, deviceMeTelemetryPath, body, headers)
	require.Equal(t, http.StatusAccepted, response.status)
	assert.Empty(t, response.body)
	assert.Equal(t, "no-store", response.header.Get("Cache-Control"))
	assert.Equal(t, http.StatusAccepted, f.post(t, body).status)
	conflict := f.post(t, strings.Replace(body, "0.043", "0.044", 1))
	assert.Equal(t, http.StatusConflict, conflict.status)
	f.assertStored(t, 1, 1, &f.observed)

	var eventMachine, outboxMachine string
	require.NoError(t, f.pool.QueryRow(t.Context(), `SELECT e.machine_id, o.machine_id
		FROM telemetry_events e JOIN telemetry_outbox o USING (device_id,event_id)
		WHERE e.device_id=$1 AND e.event_id=$2`, f.device.DeviceID, eventID).Scan(&eventMachine, &outboxMachine))
	assert.Equal(t, f.machineID, eventMachine)
	assert.Equal(t, f.machineID, outboxMachine)
}
