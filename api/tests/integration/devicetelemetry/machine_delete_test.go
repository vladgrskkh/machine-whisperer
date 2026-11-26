//go:build integration

package devicetelemetry_test

import (
	"context"
	"encoding/base64"
	"net/http"
	"net/http/httptrace"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	telemetrypostgres "github.com/vladgrskkh/machine-whisperer/api/internal/infrastructure/telemetry/postgres"
	"github.com/vladgrskkh/machine-whisperer/api/tests/integration/internal/testfixture"
)

func TestMachineDeletePreservesAcceptedTelemetryAndDeviceBinding(t *testing.T) {
	t.Parallel()
	f := testfixture.SetupDeviceTelemetry(t)
	owner := testfixture.Bearer(f.Factory.AccessToken)
	path := testfixture.MachinesPath + "/" + f.MachineID
	key := testfixture.Bearer(f.Device.APIKey)
	eventID := uuid.NewString()
	require.Equal(t, http.StatusAccepted, f.Post(t, observationJSON(eventID, f.Observed)).Status)

	wrongCredential := testfixture.AuthHTTPRequest(t, f.Server, http.MethodDelete, path, "", key)
	assert.Equal(t, http.StatusUnauthorized, wrongCredential.Status)
	deleted := testfixture.AuthHTTPRequest(t, f.Server, http.MethodDelete, path, "", owner)
	require.Equal(t, http.StatusNoContent, deleted.Status, deleted.Body)

	body := observationJSON(uuid.NewString(), f.Observed)
	for _, target := range []string{deviceMeTelemetryPath, testfixture.TelemetryPath(f.MachineID)} {
		response := testfixture.AuthHTTPRequest(t, f.Server, http.MethodPost, target, body, key)
		assert.Equal(t, http.StatusNotFound, response.Status, target)
		assert.Contains(t, response.Body, `"machine_not_found"`, target)
	}
	// Authentication reports an inactive binding only after verifying the key.
	wrongSecret := base64.RawURLEncoding.EncodeToString(make([]byte, 32))
	invalid := testfixture.AuthHTTPRequest(t, f.Server, http.MethodPost, deviceMeTelemetryPath,
		body, testfixture.Bearer(f.Device.DeviceID+"."+wrongSecret))
	assert.Equal(t, http.StatusUnauthorized, invalid.Status)
	f.AssertStored(t, 1, 1, &f.Observed)

	var boundMachine string
	var claimConsumed, stillUnrevoked bool
	require.NoError(t, f.Pool.QueryRow(t.Context(), `SELECT machine_id::text,claim_verifier IS NULL,revoked_at IS NULL
		FROM devices WHERE id=$1`, f.Device.DeviceID).Scan(&boundMachine, &claimConsumed, &stillUnrevoked))
	assert.Equal(t, f.MachineID, boundMachine)
	assert.True(t, claimConsumed)
	assert.True(t, stillUnrevoked)

	outbox := telemetrypostgres.NewOutbox(f.Pool)
	_, events, err := outbox.ClaimBatch(t.Context(), 10, time.Minute)
	require.NoError(t, err)
	require.Len(t, events, 1)
	assert.Equal(t, f.MachineID, events[0].MachineID)
	assert.Equal(t, f.Device.DeviceID, events[0].DeviceID)
	assert.Equal(t, eventID, events[0].EventID)
}

func TestTelemetryAuthenticatedBeforeMachineDeleteCannotIngestAfterward(t *testing.T) {
	t.Parallel()
	f := testfixture.SetupDeviceTelemetry(t)
	ctx, cancel := context.WithTimeout(t.Context(), 8*time.Second)
	defer cancel()
	tx, err := f.Pool.Begin(ctx)
	require.NoError(t, err)
	defer func() { _ = tx.Rollback(t.Context()) }()
	_, err = tx.Exec(ctx, "SELECT id FROM devices WHERE id=$1 FOR NO KEY UPDATE", f.Device.DeviceID)
	require.NoError(t, err)

	// The handler starts reading the request body after device authentication.
	continued := make(chan struct{})
	requestCtx := httptrace.WithClientTrace(ctx, &httptrace.ClientTrace{
		Got100Continue: func() { close(continued) },
	})
	headers := testfixture.Bearer(f.Device.APIKey)
	headers.Set("Expect", "100-continue")
	type result struct {
		response testfixture.AuthHTTPResponse
		err      error
	}
	results := make(chan result, 1)
	body := observationJSON(uuid.NewString(), f.Observed)
	go func() {
		response, sendErr := testfixture.SendAuthHTTPRequest(requestCtx, f.Server.Client(), http.MethodPost,
			f.Server.URL+deviceMeTelemetryPath, body, headers)
		results <- result{response: response, err: sendErr}
	}()
	select {
	case <-continued:
	case <-ctx.Done():
		t.Fatal("telemetry handler did not start reading the authenticated request body")
	}

	response := testfixture.AuthHTTPRequest(t, f.Server, http.MethodDelete,
		testfixture.MachinesPath+"/"+f.MachineID, "", testfixture.Bearer(f.Factory.AccessToken))
	require.Equal(t, http.StatusNoContent, response.Status, response.Body)
	require.NoError(t, tx.Commit(ctx))
	select {
	case attempt := <-results:
		require.NoError(t, attempt.err)
		assert.Equal(t, http.StatusNotFound, attempt.response.Status)
		assert.Contains(t, attempt.response.Body, `"machine_not_found"`)
	case <-ctx.Done():
		t.Fatal("telemetry handler did not respond after deletion")
	}
	f.AssertStored(t, 0, 0, nil)
}
