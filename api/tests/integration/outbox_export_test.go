//go:build integration

package integration_test

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
	analyticshttp "github.com/vladgrskkh/machine-whisperer/api/internal/infrastructure/telemetry/analytics"
	telemetrypostgres "github.com/vladgrskkh/machine-whisperer/api/internal/infrastructure/telemetry/postgres"
	"github.com/vladgrskkh/machine-whisperer/api/internal/testutil"
	telemetryusecase "github.com/vladgrskkh/machine-whisperer/api/internal/usecase/telemetry"
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
	require.NoError(t, repository.Reschedule(t.Context(), oldToken, keys, time.Hour, "analytics_http"))
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
	require.NoError(t, repository.Reschedule(t.Context(), token, keys, time.Hour, "analytics_http"))
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

	config := authConfig(t)
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
}

func TestOutboxRelayRetriesAnalyticsFailure(t *testing.T) {
	t.Parallel()
	pool := testutil.Postgres(t)
	event := insertOutboxRow(t, pool, nil)
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
		assert.JSONEq(t, `{"vibration_rms_g":0.043}`, string(got["payload"]))
		if requests.Add(1) < 4 {
			http.Error(w, "sensitive analytics response", http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(server.Close)
	sender := analyticshttp.NewHTTPClient(server.URL, "test-key", time.Second)
	export := telemetryusecase.NewExport(
		telemetrypostgres.NewOutbox(pool),
		sender,
		telemetryusecase.ExportSettings{
			BatchSize: 1, Lease: outboxTestLease, RetryBase: time.Minute, RetryMax: 2 * time.Minute,
		},
		slog.New(slog.DiscardHandler),
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
	event := telemetry.Event{
		Observation: telemetry.Observation{EventID: uuid.NewString()},
		DeviceID:    uuid.NewString(),
		MachineID:   uuid.NewString(),
		ReceivedAt:  time.Now().UTC().Truncate(time.Microsecond),
	}
	_, err := pool.Exec(t.Context(), `INSERT INTO telemetry_outbox
		(device_id,event_id,machine_id,payload,created_at,delivered_at)
		VALUES ($1,$2,$3,'{"vibration_rms_g":0.043}',$4,$5)`,
		event.DeviceID, event.EventID, event.MachineID, event.ReceivedAt, deliveredAt)
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
