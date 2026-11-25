package tracing

import (
	"context"
	"strings"
	"unicode"

	"github.com/jackc/pgx/v5"
	semconv "go.opentelemetry.io/otel/semconv/v1.43.0"
	"go.opentelemetry.io/otel/trace"

	"github.com/vladgrskkh/machine-whisperer/api/internal/spanutil"
)

type postgresTracer struct{ tracer trace.Tracer }

type postgresSpanKey struct{}

type postgresSpan struct {
	span      trace.Span
	operation string
}

const unknownPostgresOperation = "QUERY"

func NewPostgresTracer(tracer trace.Tracer) pgx.QueryTracer {
	return postgresTracer{tracer: tracer}
}

func (t postgresTracer) TraceQueryStart(
	ctx context.Context,
	_ *pgx.Conn,
	data pgx.TraceQueryStartData,
) context.Context {
	if !trace.SpanContextFromContext(ctx).IsValid() {
		return ctx
	}
	operation := postgresOperation(data.SQL)
	ctx, span := t.tracer.Start(ctx, operation, trace.WithSpanKind(trace.SpanKindClient),
		trace.WithAttributes(semconv.DBSystemNamePostgreSQL, semconv.DBOperationName(operation)))
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
	spanutil.EndDatabase(query.span, data.Err)
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
