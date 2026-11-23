//go:build integration

package integration_test

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	tc "github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/vladgrskkh/machine-whisperer/analytics/internal/application"
	"github.com/vladgrskkh/machine-whisperer/analytics/internal/config"
	"github.com/vladgrskkh/machine-whisperer/analytics/internal/infrastructure/clickhouse"
)

const (
	image       = "clickhouse/clickhouse-server:26.9.36.3"
	device      = "89ce1f51-d524-4ea5-93d1-281d5f6a3873"
	machine     = "05584bee-bfb0-4521-9281-ad359b2236cb"
	first       = "741f13de-f929-4b89-974d-170a93e147ab"
	second      = "4a64c12a-cf88-40c7-b6df-e001125643ac"
	firstEvent  = `{"device_id":"` + device + `","machine_id":"` + machine + `","event_id":"` + first + `","received_at":"2026-11-03T06:31:24.765432+03:00","payload":{"schema_version":1,"event_id":"` + first + `","observed_at":"2026-11-03T06:31:23.123456+03:00","operating_state":"running","vibration_rms_g":0.75,"dominant_frequency_hz":null,"current_rms_a":1.25}}`
	secondEvent = `{"device_id":"` + device + `","machine_id":"` + machine + `","event_id":"` + second + `","received_at":"2026-11-03T03:32:01Z","payload":{"schema_version":1,"event_id":"` + second + `","observed_at":"2026-11-03T03:32:00Z","operating_state":"unknown","vibration_rms_g":0,"dominant_frequency_hz":60,"current_rms_a":null}}`
)

func TestClickHouseBatch(t *testing.T) {
	ctx := t.Context()
	env := map[string]string{
		"CLICKHOUSE_DB":       "history",
		"CLICKHOUSE_USER":     "analytics",
		"CLICKHOUSE_PASSWORD": "fixture-password",
	}
	container, err := tc.Run(
		ctx,
		image,
		tc.WithEnv(env),
		tc.WithExposedPorts("9000/tcp", "8123/tcp"),
		tc.WithWaitStrategy(wait.ForHTTP("/ping").WithPort("8123/tcp").WithStartupTimeout(2*time.Minute)),
	)
	tc.CleanupContainer(t, container)
	require.NoError(t, err)
	address, err := container.PortEndpoint(ctx, "9000/tcp", "")
	require.NoError(t, err)
	t.Setenv("CLICKHOUSE_ADDRESS", address)
	t.Setenv("CLICKHOUSE_DATABASE", env["CLICKHOUSE_DB"])
	t.Setenv("CLICKHOUSE_USER", env["CLICKHOUSE_USER"])
	t.Setenv("CLICKHOUSE_PASSWORD", env["CLICKHOUSE_PASSWORD"])
	cfg, err := config.LoadClickHouse()
	require.NoError(t, err)
	conn, err := clickhouse.Open(ctx, cfg)
	require.NoError(t, err)
	defer conn.Close()
	app, err := application.New(ctx, config.Config{IngestKey: "fixture-key"}, application.WithClickHouse(cfg))
	require.NoError(t, err)
	t.Cleanup(app.Close)
	srv := httptest.NewServer(app.Routes())
	defer srv.Close()
	const body = `{"events":[` + firstEvent + "," + secondEvent + `]}`
	status, reply := post(t, srv, body)
	assert.Equal(t, http.StatusServiceUnavailable, status)
	assert.NotContains(t, reply, "telemetry_events")

	for range 2 {
		cmd := exec.CommandContext(ctx, "go", "run", "./cmd/migrate")
		cmd.Dir = "../.."
		output, runErr := cmd.CombinedOutput()
		require.NoError(t, runErr, "%s", output)
	}

	// Keep both physical inserts visible until FINAL applies the replacement key.
	require.NoError(t, conn.Exec(ctx, "SYSTEM STOP MERGES telemetry_events"))
	require.NoError(
		t,
		conn.Exec(ctx, "ALTER TABLE telemetry_events MODIFY SETTING non_replicated_deduplication_window = 0"),
	)
	for range 2 {
		status, reply = post(t, srv, body)
		require.Equal(t, http.StatusNoContent, status, reply)
	}
	var n uint64
	require.NoError(t, conn.QueryRow(ctx, "SELECT count() FROM telemetry_events").Scan(&n))
	assert.Equal(t, uint64(4), n)
	require.NoError(t, conn.QueryRow(ctx, "SELECT count() FROM telemetry_events FINAL").Scan(&n))
	assert.Equal(t, uint64(2), n)
	var version uint8
	var eventID, deviceID, machineID, state string
	var observed, received time.Time
	var vibration float64
	var freq, current *float64
	require.NoError(t, conn.QueryRow(ctx, `
		SELECT schema_version, toString(event_id), toString(device_id), toString(machine_id),
		       observed_at, received_at, operating_state, vibration_rms_g, dominant_frequency_hz, current_rms_a
		FROM telemetry_events FINAL WHERE event_id = ?`, first).
		Scan(&version, &eventID, &deviceID, &machineID, &observed, &received, &state, &vibration, &freq, &current))
	assert.Equal(t, uint8(1), version)
	assert.Equal(t, first, eventID)
	assert.Equal(t, device, deviceID)
	assert.Equal(t, machine, machineID)
	assert.Equal(t, "2026-11-03T03:31:23.123456Z", observed.Format(time.RFC3339Nano))
	assert.Equal(t, "2026-11-03T03:31:24.765432Z", received.Format(time.RFC3339Nano))
	assert.Equal(t, "running", state)
	assert.InDelta(t, 0.75, vibration, 1e-12)
	assert.Nil(t, freq)
	require.NotNil(t, current)
	assert.InDelta(t, 1.25, *current, 1e-12)
	require.NoError(t, conn.QueryRow(ctx, `
		SELECT operating_state, vibration_rms_g, dominant_frequency_hz, current_rms_a
		FROM telemetry_events FINAL WHERE event_id = ?`, second).
		Scan(&state, &vibration, &freq, &current))
	assert.Equal(t, "unknown", state)
	assert.Zero(t, vibration)
	require.NotNil(t, freq)
	assert.InDelta(t, 60, *freq, 1e-12)
	assert.Nil(t, current)
	require.NoError(
		t,
		conn.Exec(ctx, "ALTER TABLE telemetry_events ADD CONSTRAINT fixture_reject CHECK vibration_rms_g < 0.5"),
	)
	status, reply = post(t, srv, body)
	assert.Equal(t, http.StatusServiceUnavailable, status)
	assert.NotContains(t, reply, "fixture_reject")

	app.Close()
	// This event satisfies the constraint, so a failure here verifies that the runtime connection was closed.
	status, _ = post(t, srv, `{"events":[`+secondEvent+`]}`)
	assert.Equal(t, http.StatusServiceUnavailable, status)
}

func post(t *testing.T, srv *httptest.Server, body string) (int, string) {
	t.Helper()
	req, err := http.NewRequestWithContext(
		t.Context(),
		http.MethodPost,
		srv.URL+"/internal/v1/telemetry-batches",
		strings.NewReader(body),
	)
	require.NoError(t, err)
	req.Header.Set("Authorization", "Bearer fixture-key")
	resp, err := srv.Client().Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	return resp.StatusCode, string(data)
}
