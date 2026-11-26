package tracing_test

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"

	"github.com/vladgrskkh/machine-whisperer/api/internal/infrastructure/tracing"
)

func TestPostgresQueryTracerEmitsBoundedClientSpans(t *testing.T) {
	t.Parallel()
	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	t.Cleanup(func() { assert.NoError(t, provider.Shutdown(context.WithoutCancel(t.Context()))) })
	tracer := provider.Tracer("test.postgres")
	queryTracer := tracing.NewPostgresTracer(tracer)
	ctx, parent := tracer.Start(t.Context(), "parent")
	defer parent.End()

	for _, tc := range []struct {
		sql, commandTag, operation, errorType string
		err                                   error
	}{
		{sql: " select sensitive_value FROM users WHERE email=$1", operation: "SELECT"},
		{sql: "WITH sensitive_query AS (SELECT $1) UPDATE users SET secret=$1", commandTag: "UPDATE 1", operation: "UPDATE"},
		{sql: "COPY sensitive_table FROM STDIN", operation: "QUERY"},
		{sql: "INSERT INTO users VALUES ($1)", operation: "INSERT", errorType: "database_error",
			err: errors.New("sensitive database error")},
		{sql: "UPDATE users SET secret=$1", operation: "UPDATE", errorType: "canceled", err: context.Canceled},
	} {
		queryCtx := queryTracer.TraceQueryStart(ctx, nil, pgx.TraceQueryStartData{
			SQL: tc.sql, Args: []any{"sensitive argument"},
		})
		queryTracer.TraceQueryEnd(queryCtx, nil, pgx.TraceQueryEndData{
			CommandTag: pgconn.NewCommandTag(tc.commandTag), Err: tc.err,
		})
	}
	// Startup and empty relay polls have no active parent.
	queryCtx := queryTracer.TraceQueryStart(t.Context(), nil, pgx.TraceQueryStartData{SQL: "SELECT 1"})
	queryTracer.TraceQueryEnd(queryCtx, nil, pgx.TraceQueryEndData{})

	spans := recorder.Ended()
	want := []struct{ operation, errorType string }{
		{"SELECT", ""}, {"UPDATE", ""}, {"QUERY", ""}, {"INSERT", "database_error"}, {"UPDATE", "canceled"},
	}
	require.Len(t, spans, len(want))
	for i, span := range spans {
		assert.Equal(t, want[i].operation, span.Name())
		assert.Equal(t, trace.SpanKindClient, span.SpanKind())
		assert.Equal(t, parent.SpanContext(), span.Parent())
		assert.Equal(t, "test.postgres", span.InstrumentationScope().Name)
		assert.Empty(t, span.Events())
		assert.Empty(t, span.Status().Description)
		attrs := map[string]any{"db.system.name": "postgresql", "db.operation.name": want[i].operation}
		if want[i].errorType != "" {
			attrs["error.type"] = want[i].errorType
			assert.Equal(t, codes.Error, span.Status().Code)
		} else {
			assert.Equal(t, codes.Unset, span.Status().Code)
		}
		got := make(map[string]any)
		for _, attr := range span.Attributes() {
			got[string(attr.Key)] = attr.Value.AsInterface()
		}
		assert.Equal(t, attrs, got)
	}
}
