package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/vladgrskkh/machine-whisperer/api/internal/application"
)

const (
	readHeaderTimeout = 5 * time.Second
	readTimeout       = 10 * time.Second
	shutdownTimeout   = 10 * time.Second
)

func run(ctx context.Context, address string, handler http.Handler, runWorkers func(context.Context)) error {
	listener, err := (&net.ListenConfig{}).Listen(ctx, "tcp", address)
	if err != nil {
		return fmt.Errorf("listen: %w", err)
	}
	server := &http.Server{Handler: handler, ReadHeaderTimeout: readHeaderTimeout, ReadTimeout: readTimeout}
	ctx, cancel := context.WithCancel(ctx)
	var background sync.WaitGroup
	background.Go(func() { runWorkers(ctx) })
	background.Go(func() {
		<-ctx.Done()
		shutdownCtx, stop := context.WithTimeout(context.WithoutCancel(ctx), shutdownTimeout)
		defer stop()
		if shutdownErr := server.Shutdown(shutdownCtx); shutdownErr != nil {
			slog.New(slog.NewTextHandler(os.Stderr, nil)).Error("shutdown failed", "error", shutdownErr)
			_ = server.Close()
		}
	})
	err = server.Serve(listener)
	cancel()
	background.Wait()
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("serve: %w", err)
	}
	return nil
}

func serve(ctx context.Context, address string) error {
	config, err := application.LoadConfig()
	if err != nil {
		return err
	}
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug}))
	app, err := application.New(ctx, config, application.WithDatabase(config.Database), application.WithLogger(logger))
	if err != nil {
		return err
	}
	defer app.Close()
	return run(ctx, address, app.Routes(), app.RunWorkers)
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	address := os.Getenv("API_ADDRESS")
	if address == "" {
		address = ":8080"
	}
	err := serve(ctx, address)
	stop()
	if err != nil {
		slog.New(slog.NewTextHandler(os.Stderr, nil)).Error("api stopped", "error", err)
		os.Exit(1)
	}
}
