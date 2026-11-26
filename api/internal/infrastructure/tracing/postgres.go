package tracing

import (
	"context"
	"strings"
	"unicode"

	"github.com/exaring/otelpgx"
	"github.com/jackc/pgx/v5"
	"go.opentelemetry.io/otel/metric/noop"
	semconv "go.opentelemetry.io/otel/semconv/v1.43.0"
	"go.opentelemetry.io/otel/trace"

	"github.com/vladgrskkh/machine-whisperer/api/internal/spanutil"
)

type postgresTracer struct {
	tracer     *otelpgx.Tracer
	statements map[string]struct{}
}

type postgresSpanKey struct{}

type postgresSpan struct {
	span      trace.Span
	operation string
}

const unknownPostgresOperation = "QUERY"

func NewPostgresTracer(provider trace.TracerProvider, statements ...string) pgx.QueryTracer {
	reviewed := make(map[string]struct{}, len(statements))
	for _, statement := range statements {
		reviewed[statement] = struct{}{}
	}
	return postgresTracer{
		tracer: otelpgx.NewTracer(
			otelpgx.WithTracerProvider(provider),
			otelpgx.WithMeterProvider(noop.NewMeterProvider()),
			otelpgx.WithDisableSQLStatementInAttributes(),
			otelpgx.WithDisableConnectionDetailsInAttributes(),
			otelpgx.WithSpanNameFunc(postgresOperation),
		),
		statements: reviewed,
	}
}

func (t postgresTracer) TraceQueryStart(
	ctx context.Context,
	conn *pgx.Conn,
	data pgx.TraceQueryStartData,
) context.Context {
	parent := trace.SpanContextFromContext(ctx)
	if !parent.IsValid() {
		return withoutPostgresSpan(ctx)
	}
	ctx = t.tracer.TraceQueryStart(ctx, conn, pgx.TraceQueryStartData{SQL: data.SQL})
	span := trace.SpanFromContext(ctx)
	if span.SpanContext().SpanID() == parent.SpanID() {
		return withoutPostgresSpan(ctx)
	}
	operation := postgresOperation(data.SQL)
	span.SetAttributes(semconv.DBOperationName(operation))
	if _, reviewed := t.statements[data.SQL]; reviewed && span.SpanContext().IsSampled() {
		span.SetAttributes(semconv.DBQueryText(data.SQL))
	}
	return context.WithValue(ctx, postgresSpanKey{}, postgresSpan{span: span, operation: operation})
}

func (postgresTracer) TraceQueryEnd(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryEndData) {
	query, ok := ctx.Value(postgresSpanKey{}).(postgresSpan)
	if !ok {
		return
	}
	// The command tag identifies the operation for statements beginning with a CTE or a comment.
	if query.operation == unknownPostgresOperation {
		if operation := postgresOperation(data.CommandTag.String()); operation != unknownPostgresOperation {
			query.span.SetName(operation)
			query.span.SetAttributes(semconv.DBOperationName(operation))
		}
	}
	// otelpgx's end hook records the database error text and event.
	spanutil.EndDatabase(query.span, data.Err)
}

func withoutPostgresSpan(ctx context.Context) context.Context {
	if ctx.Value(postgresSpanKey{}) == nil {
		return ctx
	}
	// A skipped nested query must not end the outer query's span.
	return context.WithValue(ctx, postgresSpanKey{}, nil)
}

func postgresOperation(statement string) string {
	keyword := strings.TrimSpace(statement)
	if end := strings.IndexFunc(keyword, unicode.IsSpace); end >= 0 {
		keyword = keyword[:end]
	}
	switch operation := strings.ToUpper(keyword); operation {
	case "SELECT", "INSERT", "UPDATE", "DELETE", "BEGIN", "COMMIT", "ROLLBACK":
		return operation
	default:
		return unknownPostgresOperation
	}
}
