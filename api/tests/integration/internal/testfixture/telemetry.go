//go:build integration

package testfixture

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vladgrskkh/machine-whisperer/api/internal/application"
	"github.com/vladgrskkh/machine-whisperer/api/internal/testutil"
)

type TelemetryFixture struct {
	Pool      *pgxpool.Pool
	Server    *httptest.Server
	Factory   HTTPTokenPair
	Device    FactoryEnrollment
	MachineID string
	Observed  time.Time
}

func SetupDeviceTelemetry(t *testing.T, options ...application.Option) TelemetryFixture {
	t.Helper()
	return SetupDeviceTelemetryWithConfig(t, AuthConfig(t), options...)
}

func SetupDeviceTelemetryWithConfig(
	t *testing.T,
	config application.Config,
	options ...application.Option,
) TelemetryFixture {
	t.Helper()
	pool := testutil.Postgres(t)
	options = append([]application.Option{application.WithPool(pool)}, options...)
	app, err := application.New(t.Context(), config, options...)
	require.NoError(t, err)
	t.Cleanup(app.Close)
	server := httptest.NewServer(app.Routes())
	t.Cleanup(server.Close)
	factory := StartFactorySession(t, pool, server)
	machineID := CreateEnrollmentMachine(t, server, factory.AccessToken)
	device := ProvisionEnrollment(t, server, factory.AccessToken)
	claim := AuthHTTPRequest(
		t,
		server,
		http.MethodPost,
		ClaimPath,
		ClaimBody(device, machineID),
		Bearer(factory.AccessToken),
	)
	require.Equal(t, http.StatusOK, claim.Status)
	var observed time.Time
	require.NoError(t, pool.QueryRow(t.Context(), "SELECT clock_timestamp()").Scan(&observed))
	return TelemetryFixture{
		Pool: pool, Server: server, Factory: factory,
		Device: device, MachineID: machineID, Observed: observed.UTC().Truncate(time.Second),
	}
}

func TelemetryPath(machineID string) string {
	return "/v1/machines/" + machineID + "/telemetry"
}

func (f TelemetryFixture) Post(t *testing.T, body string) AuthHTTPResponse {
	t.Helper()
	return AuthHTTPRequest(t, f.Server, http.MethodPost, TelemetryPath(f.MachineID), body, Bearer(f.Device.APIKey))
}

func (f TelemetryFixture) AssertStored(t *testing.T, identities, outbox int, cursor *time.Time) {
	t.Helper()
	var storedIdentities, storedOutbox int
	var storedCursor *time.Time
	require.NoError(t, f.Pool.QueryRow(t.Context(), `SELECT
		(SELECT count(*) FROM telemetry_events), (SELECT count(*) FROM telemetry_outbox),
		last_telemetry_observed_at FROM machines WHERE id=$1`, f.MachineID).
		Scan(&storedIdentities, &storedOutbox, &storedCursor))
	assert.Equal(t, identities, storedIdentities)
	assert.Equal(t, outbox, storedOutbox)
	if cursor == nil {
		assert.Nil(t, storedCursor)
	} else {
		require.NotNil(t, storedCursor)
		assert.WithinDuration(t, *cursor, *storedCursor, 0)
	}
}

const InsertTelemetryIdentity = `INSERT INTO telemetry_events (device_id,event_id,machine_id,observed_at,payload_hash,expires_at)
	VALUES ($1,$2,$3,$4,$5,$6)`

func TelemetryLockHasWaiter(ctx context.Context, pool *pgxpool.Pool, blockerPID int) bool {
	var blocked bool
	err := pool.QueryRow(ctx, `SELECT EXISTS (
		SELECT 1 FROM pg_stat_activity WHERE $1=ANY(pg_blocking_pids(pid))
	)`, blockerPID).Scan(&blocked)
	return err == nil && blocked
}
