//go:build integration

package integration_test

import (
	"os"
	"testing"

	"github.com/vladgrskkh/machine-whisperer/api/internal/testutil"
)

//nolint:gochecknoglobals // TestMain closes the servers shared by each suite's tests.
var (
	accountPostgres           testutil.PostgresSuite
	enrollmentPostgres        testutil.PostgresSuite
	deviceTelemetryPostgres   testutil.PostgresSuite
	machineBaselinePostgres   testutil.PostgresSuite
	telemetryDetectorPostgres testutil.PostgresSuite
	telemetryHistoryPostgres  testutil.PostgresSuite
	telemetryStoragePostgres  testutil.PostgresSuite
	outboxPostgres            testutil.PostgresSuite
)

func TestMain(m *testing.M) {
	os.Exit(testutil.RunWithPostgres(
		m,
		&accountPostgres,
		&enrollmentPostgres,
		&deviceTelemetryPostgres,
		&machineBaselinePostgres,
		&telemetryDetectorPostgres,
		&telemetryHistoryPostgres,
		&telemetryStoragePostgres,
		&outboxPostgres,
	))
}
