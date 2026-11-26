//go:build integration

package detector_test

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vladgrskkh/machine-whisperer/api/tests/integration/internal/testfixture"
)

func TestMachineDeleteHidesAndPreservesBaselineAndAlerts(t *testing.T) {
	t.Parallel()
	f := setupDetector(t)
	f.baseline(t, 2)
	f.readings(t, 0, 18, 18, 18)
	alarm := f.latestAlert(t)
	owner := testfixture.Bearer(f.Factory.AccessToken)
	path := testfixture.MachinesPath + "/" + f.MachineID
	deleted := testfixture.AuthHTTPRequest(t, f.Server, http.MethodDelete, path, "", owner)
	require.Equal(t, http.StatusNoContent, deleted.Status, deleted.Body)

	for _, check := range []struct {
		method, path, body, code string
	}{
		{http.MethodGet, path + "/baseline", "", "baseline_not_found"},
		{http.MethodPut, path + "/baseline", `{"mean_rms_g":10,"stddev_rms_g":8}`, "machine_not_found"},
		{http.MethodGet, path + "/alerts", "", "machine_not_found"},
		{http.MethodPost, acknowledgePath(alarm.ID), "", "alert_not_found"},
	} {
		response := testfixture.AuthHTTPRequest(t, f.Server, check.method, check.path, check.body, owner)
		assert.Equal(t, http.StatusNotFound, response.Status, "%s %s", check.method, check.path)
		assert.Contains(t, response.Body, `"`+check.code+`"`)
	}

	var stddev float64
	var detectorExists, unacknowledged bool
	require.NoError(t, f.Pool.QueryRow(t.Context(), `SELECT b.stddev_rms_g,
		EXISTS (SELECT 1 FROM machine_detector_state WHERE machine_id=b.machine_id), a.acknowledged_at IS NULL
		FROM machine_baselines b JOIN machine_alerts a USING (machine_id)
		WHERE b.machine_id=$1 AND a.id=$2`, f.MachineID, alarm.ID).Scan(&stddev, &detectorExists, &unacknowledged))
	assert.InDelta(t, 2.0, stddev, 0)
	assert.True(t, detectorExists)
	assert.True(t, unacknowledged)
}
