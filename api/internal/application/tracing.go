package application

import (
	"context"
	"errors"

	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
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
