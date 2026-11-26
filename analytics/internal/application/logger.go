package application

import (
	"context"
	"errors"
	"io"
	"log/slog"

	"go.opentelemetry.io/otel/trace"
)

func NewLogger(output io.Writer, environment string) *slog.Logger {
	if environment == "" {
		environment = "dev"
	}
	return slog.New(traceLogHandler{slog.NewJSONHandler(output, nil)}).
		With("app", "analytics", "env", environment)
}

type traceLogHandler struct{ slog.Handler }

func (h traceLogHandler) Handle(ctx context.Context, record slog.Record) error {
	if span := trace.SpanContextFromContext(ctx); span.IsValid() && span.IsSampled() {
		record = record.Clone()
		record.AddAttrs(
			slog.String("trace_id", span.TraceID().String()),
			slog.String("span_id", span.SpanID().String()),
		)
	}
	return h.Handler.Handle(ctx, record)
}

func (h traceLogHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return traceLogHandler{h.Handler.WithAttrs(attrs)}
}

func (h traceLogHandler) WithGroup(name string) slog.Handler {
	return traceLogHandler{h.Handler.WithGroup(name)}
}

func WithLogger(logger *slog.Logger) Option {
	return func(_ context.Context, app *Application) error {
		if logger == nil {
			return errors.New("logger is required")
		}
		app.logger = logger
		return nil
	}
}
