package spanutil

import (
	"context"
	"errors"

	"go.opentelemetry.io/otel/codes"
	semconv "go.opentelemetry.io/otel/semconv/v1.43.0"
	"go.opentelemetry.io/otel/trace"
)

func End(span trace.Span, err error, expected ...error) {
	if err != nil && span.IsRecording() {
		for _, outcome := range expected {
			if errors.Is(err, outcome) {
				err = nil
				break
			}
		}
	}
	EndWithErrorType(span, err, "internal")
}

func EndDatabase(span trace.Span, err error) {
	EndWithErrorType(span, err, "database_error")
}

func EndWithErrorType(span trace.Span, err error, failure string) {
	if err != nil && span.IsRecording() {
		switch {
		case errors.Is(err, context.Canceled):
			failure = "canceled"
		case errors.Is(err, context.DeadlineExceeded):
			failure = "deadline_exceeded"
		}
		span.SetAttributes(semconv.ErrorTypeKey.String(failure))
		span.SetStatus(codes.Error, "")
	}
	span.End()
}
