package main

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/vladgrskkh/machine-whisperer/analytics/internal/application"
	"github.com/vladgrskkh/machine-whisperer/analytics/internal/config"
)

const (
	headerTimeout   = 5 * time.Second
	readTimeout     = 15 * time.Second
	writeTimeout    = 45 * time.Second
	idleTimeout     = time.Minute
	shutdownTimeout = 10 * time.Second
)

func run(ctx context.Context, logger *slog.Logger) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	app, err := application.New(ctx, cfg, application.WithClickHouse(cfg.ClickHouse))
	if err != nil {
		return err
	}
	defer app.Close()
	server := &http.Server{
		Addr:              cfg.Address,
		Handler:           app.Routes(),
		BaseContext:       func(net.Listener) context.Context { return ctx },
		ReadHeaderTimeout: headerTimeout,
		ReadTimeout:       readTimeout,
		WriteTimeout:      writeTimeout,
		IdleTimeout:       idleTimeout,
	}
	result := make(chan error, 1)
	go func() { result <- server.ListenAndServe() }()
	logger.InfoContext(ctx, "analytics server started")
	select {
	case err = <-result:
		if !errors.Is(err, http.ErrServerClosed) {
			return errors.New("analytics HTTP server failed")
		}
	case <-ctx.Done():
		shutdown, cancel := context.WithTimeout(context.WithoutCancel(ctx), shutdownTimeout)
		defer cancel()
		if err = server.Shutdown(shutdown); err != nil {
			_ = server.Close()
			return errors.New("analytics HTTP shutdown failed")
		}
	}
	logger.InfoContext(ctx, "analytics server stopped")
	return nil
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	logger := slog.New(slog.NewJSONHandler(os.Stderr, nil))
	err := run(ctx, logger)
	stop()
	if err != nil {
		logger.Error("analytics server stopped", "error", err)
		os.Exit(1)
	}
}
