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
	const reviewedStatement = "SELECT id FROM machines WHERE id=$1"
	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	t.Cleanup(func() { assert.NoError(t, provider.Shutdown(context.WithoutCancel(t.Context()))) })
	tracer := provider.Tracer("test.postgres")
	queryTracer := tracing.NewPostgresTracer(provider, reviewedStatement)
	ctx, parent := tracer.Start(t.Context(), "parent")
	defer parent.End()

	for _, tc := range []struct {
		sql, commandTag, operation, errorType string
		err                                   error
	}{
		{sql: reviewedStatement, operation: "SELECT"},
		{sql: reviewedStatement + " AND name='sensitive literal'", operation: "SELECT"},
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
	want := []struct{ operation, errorType, queryText string }{
		{"SELECT", "", reviewedStatement}, {"SELECT", "", ""}, {"SELECT", "", ""},
		{"UPDATE", "", ""}, {"QUERY", "", ""}, {"INSERT", "database_error", ""}, {"UPDATE", "canceled", ""},
	}
	require.Len(t, spans, len(want))
	for i, span := range spans {
		assert.Equal(t, want[i].operation, span.Name())
		assert.Equal(t, trace.SpanKindClient, span.SpanKind())
		assert.Equal(t, parent.SpanContext(), span.Parent())
		assert.Equal(t, "github.com/exaring/otelpgx", span.InstrumentationScope().Name)
		assert.Empty(t, span.Events())
		assert.Empty(t, span.Status().Description)
		attrs := map[string]any{"db.system.name": "postgresql", "db.operation.name": want[i].operation}
		if want[i].queryText != "" {
			attrs["db.query.text"] = want[i].queryText
		}
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

func TestPostgresQueryTracerScopesNestedSpanEnds(t *testing.T) {
	t.Parallel()
	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	t.Cleanup(func() { assert.NoError(t, provider.Shutdown(context.WithoutCancel(t.Context()))) })
	queryTracer := tracing.NewPostgresTracer(provider)
	ctx, parent := provider.Tracer("test").Start(t.Context(), "request")
	defer parent.End()

	query := pgx.TraceQueryStartData{SQL: "SELECT 1"}
	outer := queryTracer.TraceQueryStart(ctx, nil, query)
	outerSpan := trace.SpanContextFromContext(outer)
	inner := queryTracer.TraceQueryStart(outer, nil, query)
	queryTracer.TraceQueryEnd(inner, nil, pgx.TraceQueryEndData{})
	spans := recorder.Ended()
	require.Len(t, spans, 1)
	assert.Equal(t, trace.SpanContextFromContext(inner), spans[0].SpanContext())
	assert.Equal(t, outerSpan, spans[0].Parent())

	for _, tc := range []struct {
		name   string
		parent trace.SpanContext
	}{
		{name: "non-recording sampled parent", parent: outerSpan},
		{name: "unsampled parent", parent: outerSpan.WithTraceFlags(0)},
		{name: "invalid parent"},
	} {
		nested := queryTracer.TraceQueryStart(trace.ContextWithSpanContext(outer, tc.parent), nil, query)
		queryTracer.TraceQueryEnd(nested, nil, pgx.TraceQueryEndData{Err: context.Canceled})
		require.Len(t, recorder.Ended(), 1, tc.name)
	}

	queryTracer.TraceQueryEnd(outer, nil, pgx.TraceQueryEndData{})
	spans = recorder.Ended()
	require.Len(t, spans, 2)
	assert.Equal(t, outerSpan, spans[1].SpanContext())
	assert.Equal(t, codes.Unset, spans[1].Status().Code)
}
