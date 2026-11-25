//go:build integration

package baseline_test

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vladgrskkh/machine-whisperer/api/internal/testutil"
	"github.com/vladgrskkh/machine-whisperer/api/tests/integration/internal/testfixture"
)

func TestMachineBaselineHTTP(t *testing.T) {
	t.Parallel()
	pool := testutil.Postgres(t)
	server, _ := testfixture.NewSessionServer(t, pool)
	_, owner := testfixture.SignupEnrollmentUser(t, server, testfixture.SessionSignupBody)
	_, other := testfixture.SignupEnrollmentUser(
		t,
		server,
		`{"email":"other@example.com","password":"long enough password"}`,
	)
	ownerAuth, otherAuth := testfixture.Bearer(owner.AccessToken), testfixture.Bearer(other.AccessToken)
	baselinePath := func(machineID string) string { return testfixture.MachinesPath + "/" + machineID + "/baseline" }
	const (
		initialBody = `{"mean_rms_g":0,"stddev_rms_g":0.05}`
		changedBody = `{"mean_rms_g":0.4,"stddev_rms_g":0.05}`
	)

	t.Run("authentication", func(t *testing.T) {
		t.Parallel()
		machineID := testfixture.CreateEnrollmentMachine(t, server, owner.AccessToken)
		path := baselinePath(machineID)
		for _, method := range []string{http.MethodGet, http.MethodPut} {
			response := testfixture.AuthHTTPRequest(t, server, method, path, initialBody, nil)
			assert.Equal(t, http.StatusUnauthorized, response.Status)
			assert.Equal(t, "no-store", response.Header.Get("Cache-Control"))
		}
	})

	t.Run("validation", func(t *testing.T) {
		t.Parallel()
		machineID := testfixture.CreateEnrollmentMachine(t, server, owner.AccessToken)
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
			response := testfixture.AuthHTTPRequest(t, server, http.MethodPut, path, body, ownerAuth)
			assert.Equal(t, http.StatusBadRequest, response.Status, body)
		}
	})

	t.Run("owner_read_write_and_version", func(t *testing.T) {
		t.Parallel()
		machineID := testfixture.CreateEnrollmentMachine(t, server, owner.AccessToken)
		path := baselinePath(machineID)
		response := testfixture.AuthHTTPRequest(t, server, http.MethodGet, path, "", ownerAuth)
		assert.Equal(t, http.StatusNotFound, response.Status)
		first := testfixture.ReadMachineBaseline(
			t,
			testfixture.AuthHTTPRequest(t, server, http.MethodPut, path, initialBody, ownerAuth),
		)
		assert.Equal(t, machineID, first.MachineID)
		assert.Zero(t, first.MeanRMSG)
		assert.InDelta(t, 0.05, first.StddevRMSG, 0)
		assert.EqualValues(t, 1, first.Version)
		assert.False(t, first.UpdatedAt.IsZero())
		stored := testfixture.ReadMachineBaseline(
			t,
			testfixture.AuthHTTPRequest(t, server, http.MethodGet, path, "", ownerAuth),
		)
		assert.Equal(t, first, stored)

		changed := testfixture.ReadMachineBaseline(t, testfixture.AuthHTTPRequest(
			t, server, http.MethodPut, path, changedBody, ownerAuth,
		))
		assert.InDelta(t, 0.4, changed.MeanRMSG, 0)
		assert.InDelta(t, 0.05, changed.StddevRMSG, 0)
		assert.EqualValues(t, 2, changed.Version)
		identical := testfixture.ReadMachineBaseline(t, testfixture.AuthHTTPRequest(
			t, server, http.MethodPut, path, changedBody, ownerAuth,
		))
		assert.Equal(t, changed, identical)
		stddevOnly := testfixture.ReadMachineBaseline(t, testfixture.AuthHTTPRequest(
			t, server, http.MethodPut, path, `{"mean_rms_g":0.4,"stddev_rms_g":0.1}`, ownerAuth,
		))
		assert.EqualValues(t, 3, stddevOnly.Version)
		assert.InDelta(t, 0.4, stddevOnly.MeanRMSG, 0)
		assert.InDelta(t, 0.1, stddevOnly.StddevRMSG, 0)
		assert.True(t, stddevOnly.UpdatedAt.After(changed.UpdatedAt))
	})

	t.Run("write_waits_for_machine_lock", func(t *testing.T) {
		t.Parallel()
		machineID := testfixture.CreateEnrollmentMachine(t, server, owner.AccessToken)
		path := baselinePath(machineID)
		testfixture.ReadMachineBaseline(
			t,
			testfixture.AuthHTTPRequest(t, server, http.MethodPut, path, changedBody, ownerAuth),
		)
		tx, err := pool.Begin(t.Context())
		require.NoError(t, err)
		defer tx.Rollback(context.WithoutCancel(t.Context()))
		_, err = tx.Exec(t.Context(), `SELECT id FROM machines WHERE id=$1 FOR UPDATE`, machineID)
		require.NoError(t, err)

		type writeResult struct {
			response testfixture.AuthHTTPResponse
			err      error
		}
		completed := make(chan writeResult, 1)
		go func() {
			response, requestErr := testfixture.SendAuthHTTPRequest(
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
		stored := testfixture.ReadMachineBaseline(t, result.response)
		assert.EqualValues(t, 1, stored.Version)
	})

	t.Run("other_owner_isolation", func(t *testing.T) {
		t.Parallel()
		machineID := testfixture.CreateEnrollmentMachine(t, server, owner.AccessToken)
		path := baselinePath(machineID)
		missingPath := baselinePath(uuid.NewString())
		testfixture.ReadMachineBaseline(
			t,
			testfixture.AuthHTTPRequest(t, server, http.MethodPut, path, changedBody, ownerAuth),
		)
		for _, method := range []string{http.MethodGet, http.MethodPut} {
			response := testfixture.AuthHTTPRequest(t, server, method, path, initialBody, otherAuth)
			missing := testfixture.AuthHTTPRequest(t, server, method, missingPath, initialBody, otherAuth)
			assert.Equal(t, http.StatusNotFound, response.Status)
			assert.Equal(t, http.StatusNotFound, missing.Status)
			assert.Equal(t, missing.Body, response.Body)
		}
		stored := testfixture.ReadMachineBaseline(
			t,
			testfixture.AuthHTTPRequest(t, server, http.MethodGet, path, "", ownerAuth),
		)
		assert.InDelta(t, 0.4, stored.MeanRMSG, 0)
		assert.EqualValues(t, 1, stored.Version)
	})
}
