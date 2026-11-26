package application

import (
	"context"
	"errors"

	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"

	"github.com/vladgrskkh/machine-whisperer/api/internal/handler/mobiletrace"
)

func WithTracing(provider trace.TracerProvider, propagator propagation.TextMapPropagator) Option {
	return func(_ context.Context, app *Application) error {
		if provider == nil || propagator == nil {
			return errors.New("tracing requires a provider and propagator")
		}
		app.tracerProvider = provider
		app.propagator = propagator
		return nil
	}
}

func WithMobileTrace(handler *mobiletrace.Handler) Option {
	return func(_ context.Context, app *Application) error {
		app.mobileTrace = handler
		return nil
	}
}
