package application

import (
	"context"
	"errors"
	"log/slog"
)

func WithLogger(logger *slog.Logger) Option {
	return func(_ context.Context, app *Application) error {
		if logger == nil {
			return errors.New("logger is required")
		}
		app.logger = logger
		return nil
	}
}
