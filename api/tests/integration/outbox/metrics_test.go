//go:build integration

package outbox_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/vladgrskkh/machine-whisperer/api/internal/application"
	"github.com/vladgrskkh/machine-whisperer/api/internal/testutil"
	"github.com/vladgrskkh/machine-whisperer/api/tests/integration/internal/testfixture"
)

func TestOutboxSnapshotMetricRetainsLastSuccess(t *testing.T) {
	t.Parallel()
	pool := testutil.Postgres(t)
	app, err := application.New(t.Context(), testfixture.AuthConfig(t), application.WithPool(pool))
	require.NoError(t, err)
	t.Cleanup(app.Close)
	server := testfixture.NewAPIMetricsServer(t, app)

	snapshot := func(sample string) string {
		ctx, cancel := context.WithCancel(t.Context())
		stopped := make(chan struct{})
		go func() {
			defer close(stopped)
			app.RunWorkers(ctx)
		}()
		defer func() {
			cancel()
			<-stopped
		}()
		return testfixture.WaitForAPIMetrics(t, server, sample)
	}

	initial := testfixture.APIMetrics(t, server)
	assert.Contains(t, initial, "mw_outbox_snapshot_timestamp_seconds 0\n")
	assert.NotContains(t, initial, "mw_outbox_pending_events")
	empty := snapshot("mw_outbox_pending_events 0\n")
	assert.Contains(t, empty, "mw_outbox_oldest_pending_age_seconds 0\n")
	assert.Contains(t, empty, "mw_outbox_retry_events 0\n")
	assert.Contains(t, empty, "mw_outbox_oldest_retry_age_seconds 0\n")
	assert.NotContains(t, empty, "mw_outbox_snapshot_timestamp_seconds 0\n")

	delivered := time.Now()
	insertOutboxRow(t, pool, &delivered)
	retried := insertOutboxRow(t, pool, nil)
	insertOutboxRow(t, pool, nil)
	_, err = pool.Exec(t.Context(), `UPDATE telemetry_outbox
		SET created_at=statement_timestamp()-interval '3 minutes',
		    available_at=statement_timestamp()+interval '1 hour',last_error_category='analytics_http'
		WHERE event_id=$1`, retried.EventID)
	require.NoError(t, err)
	pending := snapshot("mw_outbox_pending_events 2\n")
	assert.Contains(t, pending, "mw_outbox_retry_events 1\n")
	assert.NotContains(t, pending, "mw_outbox_oldest_pending_age_seconds 0\n")
	assert.NotContains(t, pending, "mw_outbox_oldest_retry_age_seconds 0\n")

	_, err = pool.Exec(t.Context(), "DROP TABLE telemetry_outbox")
	require.NoError(t, err)
	stale := snapshot("mw_outbox_snapshot_errors_total 1\n")
	for line := range strings.SplitSeq(pending, "\n") {
		if strings.HasPrefix(line, "mw_outbox_") && !strings.HasPrefix(line, "mw_outbox_snapshot_errors_total ") {
			assert.Contains(t, stale, line+"\n")
		}
	}
	assert.Contains(t, stale, "# TYPE go_goroutines gauge")
}
