//go:build integration

package outbox_test

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vladgrskkh/machine-whisperer/api/internal/application"
	"github.com/vladgrskkh/machine-whisperer/api/internal/domain/telemetry"
	"github.com/vladgrskkh/machine-whisperer/api/internal/infrastructure/metrics"
	analyticshttp "github.com/vladgrskkh/machine-whisperer/api/internal/infrastructure/telemetry/analytics"
	telemetrypostgres "github.com/vladgrskkh/machine-whisperer/api/internal/infrastructure/telemetry/postgres"
	"github.com/vladgrskkh/machine-whisperer/api/internal/testutil"
	telemetryusecase "github.com/vladgrskkh/machine-whisperer/api/internal/usecase/telemetry"
	"github.com/vladgrskkh/machine-whisperer/api/tests/integration/internal/testfixture"
)

const outboxTestLease = time.Minute

func TestOutboxLeaseClaimsRowsOnce(t *testing.T) {
	t.Parallel()
	pool := testutil.Postgres(t)
	first := insertOutboxRow(t, pool, nil)
	second := insertOutboxRow(t, pool, nil)
	withoutLock := insertOutboxRow(t, pool, nil)
	repository := telemetrypostgres.NewOutbox(pool)
	blocker, err := pool.Begin(t.Context())
	require.NoError(t, err)
	defer func() { _ = blocker.Rollback(t.Context()) }()
	_, err = blocker.Exec(t.Context(), "SELECT 1 FROM telemetry_outbox WHERE event_id=$1 FOR UPDATE", first.EventID)
	require.NoError(t, err)

	token, events, err := repository.ClaimBatch(t.Context(), 1, outboxTestLease)
	require.NoError(t, err)
	require.Len(t, events, 1)
	assert.Equal(t, second.EventID, events[0].EventID)
	assert.WithinDuration(t, second.ReceivedAt, events[0].ReceivedAt, 0)
	assert.NotEmpty(t, token)

	nextToken, events, err := repository.ClaimBatch(t.Context(), 2, outboxTestLease)
	require.NoError(t, err)
	require.Len(t, events, 1)
	assert.Equal(t, withoutLock.EventID, events[0].EventID)
	assert.NotEqual(t, token, nextToken)
}

func TestOutboxLeaseCanBeReclaimed(t *testing.T) {
	t.Parallel()
	pool := testutil.Postgres(t)
	event := insertOutboxRow(t, pool, nil)
	repository := telemetrypostgres.NewOutbox(pool)
	oldToken, events, err := repository.ClaimBatch(t.Context(), 1, outboxTestLease)
	require.NoError(t, err)
	require.Len(t, events, 1)
	assertOutboxLeaseEligibility(t, pool, event.EventID)
	_, events, err = repository.ClaimBatch(t.Context(), 1, outboxTestLease)
	require.NoError(t, err)
	assert.Empty(t, events)

	expireOutboxLease(t, pool, event.EventID)
	newToken, events, err := repository.ClaimBatch(t.Context(), 1, outboxTestLease)
	require.NoError(t, err)
	require.Len(t, events, 1)
	assert.Equal(t, event.EventID, events[0].EventID)
	assert.NotEqual(t, oldToken, newToken)
	assert.EqualValues(t, 2, events[0].AttemptCount)
	assertOutboxLeaseEligibility(t, pool, event.EventID)
}

func TestOutboxStaleLeaseCannotAcknowledge(t *testing.T) {
	t.Parallel()
	pool := testutil.Postgres(t)
	event := insertOutboxRow(t, pool, nil)
	other := insertOutboxRow(t, pool, nil)
	repository := telemetrypostgres.NewOutbox(pool)
	oldToken, _, err := repository.ClaimBatch(t.Context(), 1, outboxTestLease)
	require.NoError(t, err)
	expireOutboxLease(t, pool, event.EventID)
	keys := []telemetry.EventKey{{DeviceID: event.DeviceID, EventID: event.EventID}}
	updated, err := repository.MarkDelivered(t.Context(), oldToken, keys)
	require.NoError(t, err)
	assert.Zero(t, updated)

	newToken, _, err := repository.ClaimBatch(t.Context(), 2, outboxTestLease)
	require.NoError(t, err)
	updated, err = repository.MarkDelivered(t.Context(), oldToken, keys)
	require.NoError(t, err)
	assert.Zero(t, updated)
	rescheduled, err := repository.Reschedule(t.Context(), oldToken, keys, time.Hour, "analytics_http")
	require.NoError(t, err)
	assert.Zero(t, rescheduled)
	updated, err = repository.MarkDelivered(t.Context(), newToken, keys)
	require.NoError(t, err)
	assert.EqualValues(t, 1, updated)
	var otherDelivered bool
	require.NoError(t, pool.QueryRow(t.Context(),
		"SELECT delivered_at IS NOT NULL FROM telemetry_outbox WHERE event_id=$1", other.EventID).Scan(&otherDelivered))
	assert.False(t, otherDelivered)
}

func TestOutboxRetryBackoff(t *testing.T) {
	t.Parallel()
	pool := testutil.Postgres(t)
	event := insertOutboxRow(t, pool, nil)
	repository := telemetrypostgres.NewOutbox(pool)
	token, _, err := repository.ClaimBatch(t.Context(), 1, outboxTestLease)
	require.NoError(t, err)
	keys := []telemetry.EventKey{{DeviceID: event.DeviceID, EventID: event.EventID}}
	rescheduled, err := repository.Reschedule(t.Context(), token, keys, time.Hour, "analytics_http")
	require.NoError(t, err)
	assert.EqualValues(t, 1, rescheduled)
	_, events, err := repository.ClaimBatch(t.Context(), 1, outboxTestLease)
	require.NoError(t, err)
	assert.Empty(t, events)

	var delaySeconds float64
	var released bool
	var category string
	require.NoError(
		t,
		pool.QueryRow(t.Context(), `SELECT extract(epoch from (available_at-now()))::float8,
		lease_token IS NULL AND lease_until IS NULL,last_error_category
		FROM telemetry_outbox WHERE event_id=$1`, event.EventID).Scan(&delaySeconds, &released, &category),
	)
	assert.InDelta(t, time.Hour.Seconds(), delaySeconds, 10)
	assert.True(t, released)
	assert.Equal(t, "analytics_http", category)
	makeOutboxDue(t, pool, event.EventID)
	_, events, err = repository.ClaimBatch(t.Context(), 1, outboxTestLease)
	require.NoError(t, err)
	require.Len(t, events, 1)
	assert.Equal(t, event.EventID, events[0].EventID)
	assertOutboxLeaseEligibility(t, pool, event.EventID)
}

func TestOutboxDeliveredRetentionIsBounded(t *testing.T) {
	t.Parallel()
	pool := testutil.Postgres(t)
	old := time.Now().AddDate(0, 0, -40)
	recent := time.Now()
	insertOutboxRow(t, pool, &old)
	insertOutboxRow(t, pool, &old)
	insertOutboxRow(t, pool, &recent)
	insertOutboxRow(t, pool, nil)

	const retention = 30 * 24 * time.Hour
	repository := telemetrypostgres.NewOutbox(pool)
	for _, expected := range []int64{1, 1, 0} {
		count, err := repository.DeleteDeliveredBefore(t.Context(), retention, 1)
		require.NoError(t, err)
		assert.Equal(t, expected, count)
	}
	var delivered, undelivered int
	require.NoError(
		t,
		pool.QueryRow(t.Context(), `SELECT count(delivered_at),count(*) FILTER (WHERE delivered_at IS NULL)
		FROM telemetry_outbox`).Scan(&delivered, &undelivered),
	)
	assert.Equal(t, 1, delivered)
	assert.Equal(t, 1, undelivered)
}

func TestApplicationRunsOutboxRelay(t *testing.T) {
	t.Parallel()
	pool := testutil.Postgres(t)
	old := time.Now().AddDate(0, 0, -40)
	insertOutboxRow(t, pool, &old)
	insertOutboxRow(t, pool, nil)
	analytics := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "Bearer test-key", r.Header.Get("Authorization"))
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(analytics.Close)

	config := testfixture.AuthConfig(t)
	config.Relay = application.RelayConfig{
		AnalyticsURL: analytics.URL, AnalyticsAPIKey: "test-key",
		HTTPTimeout: time.Second, BatchSize: 1000, PollInterval: time.Hour,
		Lease: outboxTestLease, RetryBase: time.Second, RetryMax: time.Minute, Retention: 30 * 24 * time.Hour,
	}
	app, err := application.New(t.Context(), config, application.WithPool(pool))
	require.NoError(t, err)
	t.Cleanup(app.Close)

	ctx, cancel := context.WithCancel(t.Context())
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		app.RunWorkers(ctx)
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-stopped:
		case <-time.After(2 * time.Second):
			t.Error("application worker did not stop after cancellation")
		}
	})

	require.EventuallyWithT(t, func(c *assert.CollectT) {
		var delivered, total int
		err := pool.QueryRow(ctx, "SELECT count(delivered_at), count(*) FROM telemetry_outbox").Scan(&delivered, &total)
		require.NoError(c, err)
		assert.Equal(c, 1, delivered)
		assert.Equal(c, 1, total)
	}, 3*time.Second, 20*time.Millisecond)

	metricsServer := testfixture.NewAPIMetricsServer(t, app)
	body := testfixture.WaitForAPIMetrics(t, metricsServer, "mw_outbox_delivered_events_total 1\n")
	assert.Contains(t, body, "mw_outbox_claimed_events_total 1\n")
	assert.Contains(t, body, "mw_outbox_send_attempts_total{outcome=\"success\"} 1\n")
}

func TestOutboxRelayRetriesAnalyticsFailure(t *testing.T) {
	t.Parallel()
	pool := testutil.Postgres(t)
	event := insertOutboxRow(t, pool, nil)
	var storedPayload string
	require.NoError(t, pool.QueryRow(t.Context(),
		"SELECT payload::text FROM telemetry_outbox WHERE device_id=$1 AND event_id=$2", event.DeviceID, event.EventID).
		Scan(&storedPayload))

	var requests atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodPost, r.Method)
		assert.Equal(t, "/internal/v1/telemetry-batches", r.URL.Path)
		assert.Equal(t, "Bearer test-key", r.Header.Get("Authorization"))
		assert.Equal(t, "application/json", r.Header.Get("Content-Type"))
		var body struct {
			Events []map[string]json.RawMessage `json:"events"`
		}
		if !assert.NoError(t, json.NewDecoder(r.Body).Decode(&body)) || !assert.Len(t, body.Events, 1) {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		got := body.Events[0]
		assert.Len(t, got, 5)
		assert.JSONEq(t, `"`+event.EventID+`"`, string(got["event_id"]))
		assert.JSONEq(t, `"`+event.DeviceID+`"`, string(got["device_id"]))
		assert.JSONEq(t, `"`+event.MachineID+`"`, string(got["machine_id"]))
		assert.JSONEq(t, `"`+event.ReceivedAt.Format(time.RFC3339Nano)+`"`, string(got["received_at"]))
		assert.JSONEq(t, storedPayload, string(got["payload"]))
		if requests.Add(1) < 4 {
			http.Error(w, "sensitive analytics response", http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(server.Close)
	sender := analyticshttp.NewHTTPClient(server.URL, "test-key", time.Second)
	observations := metrics.New(pool, nil)
	metricsServer := httptest.NewServer(observations.Handler())
	t.Cleanup(metricsServer.Close)
	export := telemetryusecase.NewExport(
		telemetrypostgres.NewOutbox(pool),
		sender,
		telemetryusecase.ExportSettings{
			BatchSize: 1, Lease: outboxTestLease, RetryBase: time.Minute, RetryMax: 2 * time.Minute,
		},
		slog.New(slog.DiscardHandler),
		observations,
	)
	for _, backoff := range []time.Duration{time.Minute, 2 * time.Minute, 2 * time.Minute} {
		claimed, err := export.ProcessOnce(t.Context())
		require.Error(t, err)
		assert.Equal(t, 1, claimed)
		assertOutboxRetry(t, pool, event.EventID, backoff)
		claimed, err = export.ProcessOnce(t.Context())
		require.NoError(t, err)
		assert.Zero(t, claimed)
		makeOutboxDue(t, pool, event.EventID)
	}
	claimed, err := export.ProcessOnce(t.Context())
	require.NoError(t, err)
	assert.Equal(t, 1, claimed)
	assert.EqualValues(t, 4, requests.Load())
	var delivered, cleared bool
	require.NoError(t, pool.QueryRow(t.Context(), `SELECT delivered_at IS NOT NULL,
		last_error_category IS NULL AND lease_token IS NULL AND lease_until IS NULL
		FROM telemetry_outbox WHERE event_id=$1`, event.EventID).Scan(&delivered, &cleared))
	assert.True(t, delivered)
	assert.True(t, cleared)
	body := testfixture.APIMetrics(t, metricsServer)
	assert.Contains(t, body, "mw_outbox_claimed_events_total 4\n")
	assert.Contains(t, body, "mw_outbox_delivered_events_total 1\n")
	assert.Contains(t, body, "mw_outbox_retried_events_total{reason=\"analytics_http\"} 3\n")
	assert.Contains(t, body, "mw_outbox_send_attempts_total{outcome=\"analytics_http\"} 3\n")
	assert.Contains(t, body, "mw_outbox_send_duration_seconds_count{outcome=\"analytics_http\"} 3\n")
	assert.Contains(t, body, "mw_outbox_send_attempts_total{outcome=\"success\"} 1\n")
}

func TestOutboxMetricsCountOnlyRowsChangedUnderCurrentLease(t *testing.T) {
	t.Parallel()
	pool := testutil.Postgres(t)
	event := insertOutboxRow(t, pool, nil)
	var requests atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempt := requests.Add(1)
		if attempt <= 2 {
			_, err := pool.Exec(
				r.Context(),
				"UPDATE telemetry_outbox SET available_at=now(),lease_until=now() WHERE event_id=$1",
				event.EventID,
			)
			assert.NoError(t, err)
		}
		if attempt == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(server.Close)
	observations := metrics.New(pool, nil)
	metricsServer := httptest.NewServer(observations.Handler())
	t.Cleanup(metricsServer.Close)
	export := telemetryusecase.NewExport(
		telemetrypostgres.NewOutbox(pool),
		analyticshttp.NewHTTPClient(server.URL, "test-key", time.Second),
		telemetryusecase.ExportSettings{
			BatchSize: 1, Lease: outboxTestLease, RetryBase: time.Minute, RetryMax: time.Minute,
		},
		slog.New(slog.DiscardHandler),
		observations,
	)
	claimed, err := export.ProcessOnce(t.Context())
	require.ErrorIs(t, err, telemetry.ErrAnalyticsHTTP)
	require.Equal(t, 1, claimed)
	claimed, err = export.ProcessOnce(t.Context())
	require.NoError(t, err)
	require.Equal(t, 1, claimed)
	stale := testfixture.APIMetrics(t, metricsServer)
	assert.Contains(t, stale, "mw_outbox_claimed_events_total 2\n")
	assert.Contains(t, stale, "mw_outbox_send_attempts_total{outcome=\"analytics_http\"} 1\n")
	assert.Contains(t, stale, "mw_outbox_send_attempts_total{outcome=\"success\"} 1\n")
	assert.Contains(t, stale, "mw_outbox_retried_events_total{reason=\"analytics_http\"} 0\n")
	assert.Contains(t, stale, "mw_outbox_delivered_events_total 0\n")

	claimed, err = export.ProcessOnce(t.Context())
	require.NoError(t, err)
	require.Equal(t, 1, claimed)
	acknowledged := testfixture.APIMetrics(t, metricsServer)
	assert.Contains(t, acknowledged, "mw_outbox_claimed_events_total 3\n")
	assert.Contains(t, acknowledged, "mw_outbox_send_attempts_total{outcome=\"success\"} 2\n")
	assert.Contains(t, acknowledged, "mw_outbox_delivered_events_total 1\n")
}

func assertOutboxRetry(t *testing.T, pool *pgxpool.Pool, eventID string, backoff time.Duration) {
	t.Helper()
	var delivered bool
	var category string
	var delaySeconds float64
	require.NoError(t, pool.QueryRow(t.Context(), `SELECT delivered_at IS NOT NULL,last_error_category,
		extract(epoch from (available_at-now()))::float8 FROM telemetry_outbox WHERE event_id=$1`, eventID).
		Scan(&delivered, &category, &delaySeconds))
	assert.False(t, delivered)
	assert.Equal(t, "analytics_http", category)
	assert.InDelta(t, backoff.Seconds(), delaySeconds, 10)
}

func insertOutboxRow(t *testing.T, pool *pgxpool.Pool, deliveredAt *time.Time) telemetry.Event {
	t.Helper()
	receivedAt := time.Now().UTC().Truncate(time.Microsecond)
	event := telemetry.Event{
		Observation: telemetry.Observation{
			SchemaVersion:  1,
			EventID:        uuid.NewString(),
			ObservedAt:     receivedAt.Add(-time.Second),
			OperatingState: "running",
			VibrationRMSG:  0.043,
		},
		DeviceID:   uuid.NewString(),
		MachineID:  uuid.NewString(),
		ReceivedAt: receivedAt,
	}
	payload, err := json.Marshal(event.Observation)
	require.NoError(t, err)
	_, err = pool.Exec(t.Context(), `INSERT INTO telemetry_outbox
		(device_id,event_id,machine_id,payload,created_at,delivered_at)
		VALUES ($1,$2,$3,$4,$5,$6)`,
		event.DeviceID, event.EventID, event.MachineID, payload, event.ReceivedAt, deliveredAt)
	require.NoError(t, err)
	return event
}

func assertOutboxLeaseEligibility(t *testing.T, pool *pgxpool.Pool, eventID string) {
	t.Helper()
	var availableAt, leaseUntil time.Time
	require.NoError(t, pool.QueryRow(t.Context(),
		"SELECT available_at,lease_until FROM telemetry_outbox WHERE event_id=$1", eventID).
		Scan(&availableAt, &leaseUntil))
	assert.WithinDuration(t, leaseUntil, availableAt, 0)
}

func expireOutboxLease(t *testing.T, pool *pgxpool.Pool, eventID string) {
	t.Helper()
	_, err := pool.Exec(t.Context(),
		"UPDATE telemetry_outbox SET available_at=now(),lease_until=now() WHERE event_id=$1", eventID)
	require.NoError(t, err)
}

func makeOutboxDue(t *testing.T, pool *pgxpool.Pool, eventID string) {
	t.Helper()
	_, err := pool.Exec(t.Context(), "UPDATE telemetry_outbox SET available_at=now() WHERE event_id=$1", eventID)
	require.NoError(t, err)
}
