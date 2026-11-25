//go:build integration

package outbox_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vladgrskkh/machine-whisperer/api/internal/application"
	"github.com/vladgrskkh/machine-whisperer/api/internal/testutil"
	"github.com/vladgrskkh/machine-whisperer/api/tests/integration/internal/testfixture"
)

func TestOutboxSnapshotMetricsReadCurrentQueue(t *testing.T) {
	t.Parallel()
	pool := testutil.Postgres(t)
	app, err := application.New(t.Context(), testfixture.AuthConfig(t), application.WithPool(pool))
	require.NoError(t, err)
	t.Cleanup(app.Close)
	server := testfixture.NewAPIMetricsServer(t, app)

	empty := testfixture.APIMetrics(t, server)
	assert.Contains(t, empty, "mw_outbox_pending_events 0\n")
	assert.Contains(t, empty, "mw_outbox_oldest_pending_age_seconds 0\n")
	assert.Contains(t, empty, "mw_outbox_retry_events 0\n")
	assert.Contains(t, empty, "mw_outbox_oldest_retry_age_seconds 0\n")
	assert.NotContains(t, empty, "mw_outbox_snapshot_timestamp_seconds 0\n")
	assert.Contains(t, empty, "mw_outbox_snapshot_errors_total 0\n")

	delivered := time.Now()
	insertOutboxRow(t, pool, &delivered)
	retried := insertOutboxRow(t, pool, nil)
	insertOutboxRow(t, pool, nil)
	_, err = pool.Exec(t.Context(), `UPDATE telemetry_outbox
		SET created_at=statement_timestamp()-interval '3 minutes',
		    available_at=statement_timestamp()+interval '1 hour',last_error_category='analytics_http'
		WHERE event_id=$1`, retried.EventID)
	require.NoError(t, err)
	pending := testfixture.APIMetrics(t, server)
	assert.Contains(t, pending, "mw_outbox_pending_events 2\n")
	assert.Contains(t, pending, "mw_outbox_retry_events 1\n")
	assert.NotContains(t, pending, "mw_outbox_oldest_pending_age_seconds 0\n")
	assert.NotContains(t, pending, "mw_outbox_oldest_retry_age_seconds 0\n")

	_, err = pool.Exec(t.Context(), "DROP TABLE telemetry_outbox")
	require.NoError(t, err)
	unavailable := testfixture.APIMetrics(t, server)
	assert.Contains(t, unavailable, "mw_outbox_snapshot_errors_total 1\n")
	assert.Contains(t, unavailable, "mw_outbox_snapshot_timestamp_seconds 0\n")
	assert.NotContains(t, unavailable, "mw_outbox_pending_events")
	assert.NotContains(t, unavailable, "mw_outbox_oldest_pending_age_seconds")
	assert.NotContains(t, unavailable, "mw_outbox_retry_events")
	assert.NotContains(t, unavailable, "mw_outbox_oldest_retry_age_seconds")
	assert.Contains(t, unavailable, "# TYPE go_goroutines gauge")
	assert.Contains(t, testfixture.APIMetrics(t, server), "mw_outbox_snapshot_errors_total 2\n")
}

func TestOutboxSnapshotMetricsTimesOutWaitingForQueue(t *testing.T) {
	t.Parallel()
	pool := testutil.Postgres(t)
	app, err := application.New(t.Context(), testfixture.AuthConfig(t), application.WithPool(pool))
	require.NoError(t, err)
	t.Cleanup(app.Close)
	server := testfixture.NewAPIMetricsServer(t, app)
	server.Client().Timeout = 5 * time.Second

	tx, err := pool.Begin(t.Context())
	require.NoError(t, err)
	defer func() { _ = tx.Rollback(t.Context()) }()
	_, err = tx.Exec(t.Context(), "LOCK TABLE telemetry_outbox IN ACCESS EXCLUSIVE MODE")
	require.NoError(t, err)

	unavailable := testfixture.APIMetrics(t, server)
	assert.Contains(t, unavailable, "mw_outbox_snapshot_errors_total 1\n")
	assert.Contains(t, unavailable, "mw_outbox_snapshot_timestamp_seconds 0\n")
	assert.NotContains(t, unavailable, "mw_outbox_pending_events")

	require.NoError(t, tx.Rollback(t.Context()))
	recovered := testfixture.APIMetrics(t, server)
	assert.Contains(t, recovered, "mw_outbox_snapshot_errors_total 1\n")
	assert.Contains(t, recovered, "mw_outbox_pending_events 0\n")
	assert.NotContains(t, recovered, "mw_outbox_snapshot_timestamp_seconds 0\n")
}
