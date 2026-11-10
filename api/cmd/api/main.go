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
	"syscall"
	"time"

	"github.com/go-chi/chi/v5"
)

const (
	readHeaderTimeout = 5 * time.Second
	shutdownTimeout   = 10 * time.Second
)

func newHandler() http.Handler {
	return chi.NewRouter()
}

func run(ctx context.Context, address string) error {
	listener, err := (&net.ListenConfig{}).Listen(ctx, "tcp", address)
	if err != nil {
		return fmt.Errorf("listen: %w", err)
	}
	server := &http.Server{Handler: newHandler(), ReadHeaderTimeout: readHeaderTimeout}
	done := make(chan struct{})
	shutdownDone := make(chan struct{})
	defer close(done)
	go func() {
		defer close(shutdownDone)
		select {
		case <-ctx.Done():
			shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), shutdownTimeout)
			defer cancel()
			if shutdownErr := server.Shutdown(shutdownCtx); shutdownErr != nil {
				slog.New(slog.NewTextHandler(os.Stderr, nil)).Error("shutdown failed", "error", shutdownErr)
				_ = server.Close()
			}
		case <-done:
		}
	}()
	err = server.Serve(listener)
	if errors.Is(err, http.ErrServerClosed) {
		<-shutdownDone
		return nil
	}
	if err != nil {
		return fmt.Errorf("serve: %w", err)
	}
	return nil
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	address := os.Getenv("API_ADDRESS")
	if address == "" {
		address = ":8080"
	}
	err := run(ctx, address)
	stop()
	if err != nil {
		slog.New(slog.NewTextHandler(os.Stderr, nil)).Error("api stopped", "error", err)
		os.Exit(1)
	}
}
