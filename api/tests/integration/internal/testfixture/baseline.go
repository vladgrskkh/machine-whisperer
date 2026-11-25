//go:build integration

package testfixture

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type MachineBaselineResponse struct {
	MachineID  string    `json:"machine_id"`
	MeanRMSG   float64   `json:"mean_rms_g"`
	StddevRMSG float64   `json:"stddev_rms_g"`
	Version    int64     `json:"version"`
	UpdatedAt  time.Time `json:"updated_at"`
}

func ReadMachineBaseline(t *testing.T, response AuthHTTPResponse) MachineBaselineResponse {
	t.Helper()
	require.Equal(t, http.StatusOK, response.Status, response.Body)
	assert.Equal(t, "no-store", response.Header.Get("Cache-Control"))
	var baseline MachineBaselineResponse
	require.NoError(t, json.Unmarshal([]byte(response.Body), &baseline))
	return baseline
}
