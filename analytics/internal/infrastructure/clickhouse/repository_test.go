package clickhouse_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	ch "github.com/ClickHouse/clickhouse-go/v2"
	"github.com/ClickHouse/clickhouse-go/v2/lib/driver"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
	"go.opentelemetry.io/otel/trace/noop"

	"github.com/vladgrskkh/machine-whisperer/analytics/internal/domain"
	"github.com/vladgrskkh/machine-whisperer/analytics/internal/infrastructure/clickhouse"
	"github.com/vladgrskkh/machine-whisperer/analytics/internal/infrastructure/metrics"
	"github.com/vladgrskkh/machine-whisperer/analytics/internal/usecase/telemetry"
)

func TestListRecordsFailuresAfterQueryStarts(t *testing.T) {
	t.Parallel()
	registry := metrics.New()
	repository := clickhouse.NewRepository(failedQueryConn{}, registry, noop.NewTracerProvider().Tracer("test"))
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	expired, cancelDeadline := context.WithTimeout(t.Context(), 0)
	defer cancelDeadline()
	for _, ctx := range []context.Context{t.Context(), canceled, expired} {
		_, err := repository.List(ctx, domain.HistoryQuery{})
		require.ErrorIs(t, err, io.ErrUnexpectedEOF)
	}

	response := httptest.NewRecorder()
	request := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/metrics", nil)
	registry.Handler().ServeHTTP(response, request)
	require.Equal(t, http.StatusOK, response.Code)
	body := response.Body.String()
	assert.Contains(t, body, "mw_clickhouse_operations_total{operation=\"list_history\",outcome=\"success\"} 0\n")
	for _, outcome := range []string{"error", "canceled", "timeout"} {
		assert.Contains(
			t,
			body,
			"mw_clickhouse_operations_total{operation=\"list_history\",outcome=\""+outcome+"\"} 1\n",
		)
	}
}

type failedQueryConn struct{ ch.Conn }

func (failedQueryConn) Query(context.Context, string, ...any) (driver.Rows, error) {
	return failingRows{}, nil
}

type failingRows struct{ driver.Rows }

func (failingRows) Next() bool   { return false }
func (failingRows) Err() error   { return io.ErrUnexpectedEOF }
func (failingRows) Close() error { return nil }

func TestClickHouseClientSpansCoverBatchSendAndRowConsumption(t *testing.T) {
	t.Parallel()
	for _, operation := range []string{"INSERT", "SELECT"} {
		t.Run(operation, func(t *testing.T) {
			t.Parallel()
			recorder := tracetest.NewSpanRecorder()
			provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
			t.Cleanup(func() { assert.NoError(t, provider.Shutdown(context.WithoutCancel(t.Context()))) })
			adapterTracer := provider.Tracer("adapter")
			useCaseTracer := provider.Tracer("usecase")
			ctx, parent := useCaseTracer.Start(t.Context(), "request")
			defer parent.End()
			ctx, cancel := context.WithCancel(ctx)
			cancel()
			conn := &nativeConn{t: t, recorder: recorder, err: errors.New("sensitive SQL or bound value")}
			registry := metrics.New()
			repo := clickhouse.NewRepository(conn, registry, adapterTracer)
			if operation == "INSERT" {
				err := telemetry.NewIngestBatch(repo, registry, slog.New(slog.DiscardHandler), useCaseTracer).
					Execute(ctx, []domain.Event{{EventID: "secret-event"}})
				require.ErrorIs(t, err, conn.err)
			} else {
				_, err := telemetry.NewListHistory(repo, useCaseTracer).Execute(ctx, domain.HistoryQuery{
					MachineID: "secret-machine", Limit: 1,
				})
				require.ErrorIs(t, err, conn.err)
			}

			spans := recorder.Ended()
			require.Len(t, spans, 2)
			clientSpan, useCaseSpan := spans[0], spans[1]
			assert.Equal(t, trace.SpanKindClient, clientSpan.SpanKind())
			assert.Equal(t, trace.SpanKindInternal, useCaseSpan.SpanKind())
			assert.Equal(t, useCaseSpan.SpanContext(), clientSpan.Parent())
			assert.Equal(t, parent.SpanContext(), useCaseSpan.Parent())
			assert.Equal(t, conn.dbSpan, clientSpan.SpanContext())
			assert.Equal(t, operation+" telemetry_events", clientSpan.Name())
			wantUseCase := "telemetry.batch.ingest"
			if operation == "SELECT" {
				wantUseCase = "telemetry.history.list"
			}
			assert.Equal(t, wantUseCase, useCaseSpan.Name())
			assert.Equal(t, map[string]any{
				"db.system.name": "clickhouse", "db.operation.name": operation, "error.type": "database_error",
				"db.query.summary": operation + " telemetry_events",
			}, databaseAttributes(clientSpan))
			assert.Equal(t, map[string]any{"error.type": "internal"}, databaseAttributes(useCaseSpan))
			for _, span := range spans {
				assert.Equal(t, codes.Error, span.Status().Code)
				assert.Empty(t, span.Status().Description)
				assert.Empty(t, span.Events())
			}
		})
	}
}

type nativeConn struct {
	ch.Conn

	t        *testing.T
	recorder *tracetest.SpanRecorder
	dbSpan   trace.SpanContext
	err      error
}

func (c *nativeConn) PrepareBatch(ctx context.Context, _ string, _ ...driver.PrepareBatchOption) (driver.Batch, error) {
	c.capture(ctx)
	return nativeBatch{conn: c}, nil
}

func (c *nativeConn) Query(ctx context.Context, _ string, _ ...any) (driver.Rows, error) {
	c.capture(ctx)
	return nativeRows{conn: c}, nil
}

func (c *nativeConn) capture(ctx context.Context) {
	c.t.Helper()
	c.dbSpan = trace.SpanContextFromContext(ctx)
	assert.ErrorIs(c.t, ctx.Err(), context.Canceled)
	assert.Empty(c.t, c.recorder.Ended())
}

type nativeBatch struct {
	driver.Batch

	conn *nativeConn
}

func (nativeBatch) Append(...any) error { return nil }
func (nativeBatch) Close() error        { return nil }

func (b nativeBatch) Send() error {
	assert.Empty(b.conn.t, b.conn.recorder.Ended())
	return b.conn.err
}

type nativeRows struct {
	driver.Rows

	conn *nativeConn
}

func (r nativeRows) Next() bool {
	assert.Empty(r.conn.t, r.conn.recorder.Ended())
	return false
}

func (r nativeRows) Err() error { return r.conn.err }
func (nativeRows) Close() error { return nil }

func databaseAttributes(span sdktrace.ReadOnlySpan) map[string]any {
	values := make(map[string]any)
	for _, attr := range span.Attributes() {
		values[string(attr.Key)] = attr.Value.AsInterface()
	}
	return values
}
