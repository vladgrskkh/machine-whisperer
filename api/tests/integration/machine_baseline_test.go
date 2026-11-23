//go:build integration

package integration_test

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vladgrskkh/machine-whisperer/api/internal/testutil"
)

func TestMachineBaselineHTTP(t *testing.T) {
	t.Parallel()
	pool := testutil.Postgres(t)
	server, _ := newSessionServer(t, pool)
	_, owner := signupEnrollmentUser(t, server, httpCredentials)
	_, other := signupEnrollmentUser(t, server, `{"email":"other@example.com","password":"long enough password"}`)
	ownerAuth, otherAuth := bearer(owner.AccessToken), bearer(other.AccessToken)
	baselinePath := func(machineID string) string { return machinesPath + "/" + machineID + "/baseline" }
	const (
		initialBody = `{"mean_rms_g":0,"stddev_rms_g":0.05}`
		changedBody = `{"mean_rms_g":0.4,"stddev_rms_g":0.05}`
	)

	t.Run("authentication", func(t *testing.T) {
		t.Parallel()
		machineID := createEnrollmentMachine(t, server, owner.AccessToken)
		path := baselinePath(machineID)
		for _, method := range []string{http.MethodGet, http.MethodPut} {
			response := authHTTPRequest(t, server, method, path, initialBody, nil)
			assert.Equal(t, http.StatusUnauthorized, response.status)
			assert.Equal(t, "no-store", response.header.Get("Cache-Control"))
		}
	})

	t.Run("validation", func(t *testing.T) {
		t.Parallel()
		machineID := createEnrollmentMachine(t, server, owner.AccessToken)
		path := baselinePath(machineID)
		for _, body := range []string{
			`{"mean_rms_g":-0.1,"stddev_rms_g":0.05}`,
			`{"mean_rms_g":0.4,"stddev_rms_g":0}`,
			`{"mean_rms_g":0.4,"stddev_rms_g":-0.05}`,
			`{"mean_rms_g":1e309,"stddev_rms_g":0.05}`,
			`{"mean_rms_g":0.4,"stddev_rms_g":1e309}`,
			`{"mean_rms_g":"NaN","stddev_rms_g":0.05}`,
			`{"mean_rms_g":null,"stddev_rms_g":0.05}`,
			`{"stddev_rms_g":0.05}`,
			`{"mean_rms_g":0.4}`,
			`{"mean_rms_g":0,"mean_rms_g":1,"stddev_rms_g":0.05}`,
		} {
			response := authHTTPRequest(t, server, http.MethodPut, path, body, ownerAuth)
			assert.Equal(t, http.StatusBadRequest, response.status, body)
		}
	})

	t.Run("owner_read_write_and_version", func(t *testing.T) {
		t.Parallel()
		machineID := createEnrollmentMachine(t, server, owner.AccessToken)
		path := baselinePath(machineID)
		response := authHTTPRequest(t, server, http.MethodGet, path, "", ownerAuth)
		assert.Equal(t, http.StatusNotFound, response.status)
		first := readMachineBaseline(t, authHTTPRequest(t, server, http.MethodPut, path, initialBody, ownerAuth))
		assert.Equal(t, machineID, first.MachineID)
		assert.Zero(t, first.MeanRMSG)
		assert.InDelta(t, 0.05, first.StddevRMSG, 0)
		assert.EqualValues(t, 1, first.Version)
		assert.False(t, first.UpdatedAt.IsZero())
		stored := readMachineBaseline(t, authHTTPRequest(t, server, http.MethodGet, path, "", ownerAuth))
		assert.Equal(t, first, stored)

		changed := readMachineBaseline(t, authHTTPRequest(
			t, server, http.MethodPut, path, changedBody, ownerAuth,
		))
		assert.InDelta(t, 0.4, changed.MeanRMSG, 0)
		assert.InDelta(t, 0.05, changed.StddevRMSG, 0)
		assert.EqualValues(t, 2, changed.Version)
		identical := readMachineBaseline(t, authHTTPRequest(
			t, server, http.MethodPut, path, changedBody, ownerAuth,
		))
		assert.Equal(t, changed, identical)
		stddevOnly := readMachineBaseline(t, authHTTPRequest(
			t, server, http.MethodPut, path, `{"mean_rms_g":0.4,"stddev_rms_g":0.1}`, ownerAuth,
		))
		assert.EqualValues(t, 3, stddevOnly.Version)
		assert.InDelta(t, 0.4, stddevOnly.MeanRMSG, 0)
		assert.InDelta(t, 0.1, stddevOnly.StddevRMSG, 0)
		assert.True(t, stddevOnly.UpdatedAt.After(changed.UpdatedAt))
	})

	t.Run("write_waits_for_machine_lock", func(t *testing.T) {
		t.Parallel()
		machineID := createEnrollmentMachine(t, server, owner.AccessToken)
		path := baselinePath(machineID)
		readMachineBaseline(t, authHTTPRequest(t, server, http.MethodPut, path, changedBody, ownerAuth))
		tx, err := pool.Begin(t.Context())
		require.NoError(t, err)
		defer tx.Rollback(context.WithoutCancel(t.Context()))
		_, err = tx.Exec(t.Context(), `SELECT id FROM machines WHERE id=$1 FOR UPDATE`, machineID)
		require.NoError(t, err)

		type writeResult struct {
			response authHTTPResponse
			err      error
		}
		completed := make(chan writeResult, 1)
		go func() {
			response, requestErr := sendAuthHTTPRequest(
				t.Context(), server.Client(), http.MethodPut, server.URL+path,
				changedBody, ownerAuth,
			)
			completed <- writeResult{response: response, err: requestErr}
		}()
		require.EventuallyWithT(t, func(collect *assert.CollectT) {
			var waiting bool
			err := pool.QueryRow(t.Context(), `SELECT EXISTS (
				SELECT 1 FROM pg_stat_activity WHERE $1::int = ANY(pg_blocking_pids(pid))
			)`, int64(tx.Conn().PgConn().PID())).Scan(&waiting)
			require.NoError(collect, err)
			assert.True(collect, waiting)
		}, 3*time.Second, 10*time.Millisecond)
		require.NoError(t, tx.Commit(t.Context()))

		result := <-completed
		require.NoError(t, result.err)
		stored := readMachineBaseline(t, result.response)
		assert.EqualValues(t, 1, stored.Version)
	})

	t.Run("other_owner_isolation", func(t *testing.T) {
		t.Parallel()
		machineID := createEnrollmentMachine(t, server, owner.AccessToken)
		path := baselinePath(machineID)
		missingPath := baselinePath(uuid.NewString())
		readMachineBaseline(t, authHTTPRequest(t, server, http.MethodPut, path, changedBody, ownerAuth))
		for _, method := range []string{http.MethodGet, http.MethodPut} {
			response := authHTTPRequest(t, server, method, path, initialBody, otherAuth)
			missing := authHTTPRequest(t, server, method, missingPath, initialBody, otherAuth)
			assert.Equal(t, http.StatusNotFound, response.status)
			assert.Equal(t, http.StatusNotFound, missing.status)
			assert.Equal(t, missing.body, response.body)
		}
		stored := readMachineBaseline(t, authHTTPRequest(t, server, http.MethodGet, path, "", ownerAuth))
		assert.InDelta(t, 0.4, stored.MeanRMSG, 0)
		assert.EqualValues(t, 1, stored.Version)
	})
}

type machineBaselineResponse struct {
	MachineID  string    `json:"machine_id"`
	MeanRMSG   float64   `json:"mean_rms_g"`
	StddevRMSG float64   `json:"stddev_rms_g"`
	Version    int64     `json:"version"`
	UpdatedAt  time.Time `json:"updated_at"`
}

func readMachineBaseline(t *testing.T, response authHTTPResponse) machineBaselineResponse {
	t.Helper()
	require.Equal(t, http.StatusOK, response.status, response.body)
	assert.Equal(t, "no-store", response.header.Get("Cache-Control"))
	var baseline machineBaselineResponse
	require.NoError(t, json.Unmarshal([]byte(response.body), &baseline))
	return baseline
}
