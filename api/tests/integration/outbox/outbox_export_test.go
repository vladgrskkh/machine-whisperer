//go:build integration

package outbox_test

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgerrcode"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/propagation"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
	"go.opentelemetry.io/otel/trace/noop"

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
	wantLinks, rows := seedMixedTraceparents(t, pool)
	analytics := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "Bearer test-key", r.Header.Get("Authorization"))
		var body struct {
			Events []json.RawMessage `json:"events"`
		}
		assert.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		assert.Len(t, body.Events, rows)
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(analytics.Close)

	config := testfixture.AuthConfig(t)
	config.Database.URL = pool.Config().ConnString()
	config.Relay = application.RelayConfig{
		AnalyticsURL: analytics.URL, AnalyticsAPIKey: "test-key",
		HTTPTimeout: time.Second, BatchSize: 1000, PollInterval: time.Hour,
		Lease: outboxTestLease, RetryBase: time.Second, RetryMax: time.Minute, Retention: 30 * 24 * time.Hour,
	}
	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	t.Cleanup(func() { assert.NoError(t, provider.Shutdown(context.WithoutCancel(t.Context()))) })
	app, err := application.New(t.Context(), config, application.WithDatabase(config.Database),
		application.WithTracing(provider, propagation.TraceContext{}))
	require.NoError(t, err)
	t.Cleanup(app.Close)

	parentCtx, caller := provider.Tracer("test").Start(t.Context(), "caller")
	defer caller.End()
	ctx, cancel := context.WithCancel(parentCtx)
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
		assert.Equal(c, rows, delivered)
		assert.Equal(c, rows, total)
	}, 3*time.Second, 20*time.Millisecond)
	var update, delivery, outboxSpan sdktrace.ReadOnlySpan
	require.Eventually(t, func() bool {
		update, delivery, outboxSpan = outboxRelaySpans(recorder.Ended(), rows)
		return update != nil && delivery != nil && outboxSpan != nil
	}, time.Second, time.Millisecond)
	assert.Equal(t, trace.SpanKindClient, update.SpanKind())
	assert.Equal(t, delivery.SpanContext(), update.Parent())
	assert.Equal(t, outboxSpan.SpanContext(), delivery.Parent())
	assert.False(t, outboxSpan.Parent().IsValid())
	assert.NotEqual(t, caller.SpanContext().TraceID(), outboxSpan.SpanContext().TraceID())
	assert.Contains(t, outboxSpan.Attributes(), attribute.Int("outbox.links.dropped", 1))
	require.Len(t, outboxSpan.Links(), len(wantLinks))
	gotLinks := make([]trace.SpanContext, 0, len(outboxSpan.Links()))
	for _, link := range outboxSpan.Links() {
		assert.Empty(t, link.Attributes)
		gotLinks = append(gotLinks, link.SpanContext)
	}
	assert.Equal(t, wantLinks, gotLinks)

	metricsServer := testfixture.NewAPIMetricsServer(t, app)
	body := testfixture.WaitForAPIMetrics(t, metricsServer, fmt.Sprintf("mw_outbox_delivered_events_total %d\n", rows))
	assert.Contains(t, body, fmt.Sprintf("mw_outbox_claimed_events_total %d\n", rows))
	assert.Contains(t, body, "mw_outbox_send_attempts_total{outcome=\"success\"} 1\n")
}

func outboxRelaySpans(spans []sdktrace.ReadOnlySpan, rows int) (update, delivery, outbox sdktrace.ReadOnlySpan) {
	for _, span := range spans {
		switch span.Name() {
		case "outbox.deliver":
			outbox = span
		case "outbox.batch.deliver":
			delivery = span
		}
	}
	if delivery == nil || outbox == nil {
		return nil, delivery, outbox
	}
	for _, span := range spans {
		if span.SpanKind() != trace.SpanKindClient || !span.Parent().Equal(delivery.SpanContext()) {
			continue
		}
		attrs := span.Attributes()
		if !slices.Contains(attrs, attribute.String("db.system.name", "postgresql")) ||
			!slices.Contains(attrs, attribute.String("db.operation.name", "UPDATE")) ||
			!slices.Contains(attrs, attribute.Int("pgx.rows_affected", rows)) {
			continue
		}
		if slices.ContainsFunc(attrs, func(attr attribute.KeyValue) bool {
			return attr.Key == "db.query.text" &&
				strings.HasPrefix(attr.Value.AsString(), "UPDATE telemetry_outbox") &&
				strings.Contains(attr.Value.AsString(), "SET delivered_at=statement_timestamp()")
		}) {
			return span, delivery, outbox
		}
	}
	return nil, delivery, outbox
}

func seedMixedTraceparents(t *testing.T, pool *pgxpool.Pool) ([]trace.SpanContext, int) {
	t.Helper()
	const traceID = "12345678901234567890123456789012"
	values := make([]string, 0, 39)
	want := make([]trace.SpanContext, 0, 32)
	propagator := propagation.TraceContext{}
	for index := range 33 {
		value := fmt.Sprintf("00-%s-%016x-01", traceID, index+1)
		values = append(values, value)
		if index < 32 {
			ctx := propagator.Extract(t.Context(), propagation.MapCarrier{"traceparent": value})
			want = append(want, trace.SpanContextFromContext(ctx))
		}
	}
	values = append(values, values[0], values[32], "invalid", "ff-"+traceID+"-1234567890123456-01",
		"00-"+traceID+"-1234567890123456-00", "")
	for index, value := range values {
		event := insertOutboxRow(t, pool, nil)
		_, err := pool.Exec(t.Context(), `UPDATE telemetry_outbox SET
			traceparent=NULLIF($1,''),available_at=$2 WHERE event_id=$3`,
			value, time.Unix(int64(index), 0), event.EventID)
		require.NoError(t, err)
	}
	_, err := pool.Exec(t.Context(), "UPDATE telemetry_outbox SET traceparent=$1", strings.Repeat("a", 56))
	testfixture.RequirePGCode(t, err, pgerrcode.StringDataRightTruncationDataException)
	return want, len(values)
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
	outbox := telemetrypostgres.NewOutbox(pool)
	observations := metrics.New(telemetryusecase.NewMeasureOutbox(outbox))
	sender := analyticshttp.NewHTTPClient(server.URL, "test-key", time.Second, observations, http.DefaultTransport)
	metricsServer := httptest.NewServer(observations.Handler())
	t.Cleanup(metricsServer.Close)
	export := telemetryusecase.NewExport(
		outbox,
		sender,
		telemetryusecase.ExportSettings{
			BatchSize: 1, Lease: outboxTestLease, RetryBase: time.Minute, RetryMax: 2 * time.Minute,
		},
		slog.New(slog.DiscardHandler),
		observations,
		noop.NewTracerProvider().Tracer("test"),
	)
	for _, backoff := range []time.Duration{time.Minute, 2 * time.Minute, 2 * time.Minute} {
		token, events, err := export.ClaimBatch(t.Context())
		require.NoError(t, err)
		require.Len(t, events, 1)
		require.ErrorIs(t, export.DeliverBatch(t.Context(), token, events), telemetry.ErrAnalyticsHTTP)
		assertOutboxRetry(t, pool, event.EventID, backoff)
		_, events, err = export.ClaimBatch(t.Context())
		require.NoError(t, err)
		assert.Empty(t, events)
		makeOutboxDue(t, pool, event.EventID)
	}
	token, events, err := export.ClaimBatch(t.Context())
	require.NoError(t, err)
	require.Len(t, events, 1)
	require.NoError(t, export.DeliverBatch(t.Context(), token, events))
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

	require.NoError(t, sender.Send(t.Context(), []telemetry.OutboxEvent{{
		DeviceID: event.DeviceID, EventID: event.EventID, MachineID: event.MachineID,
		ReceivedAt: event.ReceivedAt, Payload: json.RawMessage(storedPayload),
	}}))
	assert.EqualValues(t, 5, requests.Load())
	body = testfixture.APIMetrics(t, metricsServer)
	assert.Contains(t, body, "mw_outbox_send_attempts_total{outcome=\"success\"} 2\n")
	assert.Contains(t, body, "mw_outbox_send_duration_seconds_count{outcome=\"success\"} 2\n")
	assert.Contains(t, body, "mw_outbox_delivered_events_total 1\n")
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
	outbox := telemetrypostgres.NewOutbox(pool)
	observations := metrics.New(telemetryusecase.NewMeasureOutbox(outbox))
	metricsServer := httptest.NewServer(observations.Handler())
	t.Cleanup(metricsServer.Close)
	export := telemetryusecase.NewExport(
		outbox,
		analyticshttp.NewHTTPClient(server.URL, "test-key", time.Second, observations, http.DefaultTransport),
		telemetryusecase.ExportSettings{
			BatchSize: 1, Lease: outboxTestLease, RetryBase: time.Minute, RetryMax: time.Minute,
		},
		slog.New(slog.DiscardHandler),
		observations,
		noop.NewTracerProvider().Tracer("test"),
	)
	token, events, err := export.ClaimBatch(t.Context())
	require.NoError(t, err)
	require.Len(t, events, 1)
	require.ErrorIs(t, export.DeliverBatch(t.Context(), token, events), telemetry.ErrAnalyticsHTTP)
	token, events, err = export.ClaimBatch(t.Context())
	require.NoError(t, err)
	require.Len(t, events, 1)
	require.NoError(t, export.DeliverBatch(t.Context(), token, events))
	stale := testfixture.APIMetrics(t, metricsServer)
	assert.Contains(t, stale, "mw_outbox_claimed_events_total 2\n")
	assert.Contains(t, stale, "mw_outbox_send_attempts_total{outcome=\"analytics_http\"} 1\n")
	assert.Contains(t, stale, "mw_outbox_send_attempts_total{outcome=\"success\"} 1\n")
	assert.Contains(t, stale, "mw_outbox_retried_events_total{reason=\"analytics_http\"} 0\n")
	assert.Contains(t, stale, "mw_outbox_delivered_events_total 0\n")

	token, events, err = export.ClaimBatch(t.Context())
	require.NoError(t, err)
	require.Len(t, events, 1)
	require.NoError(t, export.DeliverBatch(t.Context(), token, events))
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
