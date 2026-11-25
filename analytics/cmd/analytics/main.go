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

	"golang.org/x/sync/errgroup"

	"github.com/vladgrskkh/machine-whisperer/analytics/internal/application"
	"github.com/vladgrskkh/machine-whisperer/analytics/internal/config"
	"github.com/vladgrskkh/machine-whisperer/analytics/internal/infrastructure/tracing"
)

const (
	headerTimeout      = 5 * time.Second
	readTimeout        = 15 * time.Second
	writeTimeout       = 45 * time.Second
	idleTimeout        = time.Minute
	shutdownTimeout    = 10 * time.Second
	metricsTimeout     = 5 * time.Second
	metricsIdleTimeout = 30 * time.Second
)

func run(ctx context.Context, logger *slog.Logger) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	traces, err := tracing.New(ctx, "analytics")
	if err != nil {
		return err
	}
	defer func() {
		if traces.Shutdown(ctx) != nil {
			logger.WarnContext(ctx, "trace provider shutdown failed")
		}
	}()
	app, err := application.New(ctx, cfg,
		application.WithTracing(traces.Provider, traces.Propagator), application.WithClickHouse(cfg.ClickHouse),
	)
	if err != nil {
		return err
	}
	defer app.Close()
	return serveHTTP(ctx, logger, cfg, app.Routes(), app.MetricsHandler)
}

func serveHTTP(
	ctx context.Context, logger *slog.Logger, cfg config.Config, businessHandler, metricsHandler http.Handler,
) error {
	metricsAddress, err := resolveMetricsAddress(cfg.MetricsAddress)
	if err != nil {
		return err
	}
	group, ctx := errgroup.WithContext(ctx)
	baseContext := func(net.Listener) context.Context { return ctx }
	business := &http.Server{
		Addr:              cfg.Address,
		Handler:           businessHandler,
		BaseContext:       baseContext,
		ReadHeaderTimeout: headerTimeout,
		ReadTimeout:       readTimeout,
		WriteTimeout:      writeTimeout,
		IdleTimeout:       idleTimeout,
	}
	metrics := &http.Server{
		Addr:              metricsAddress,
		Handler:           metricsHandler,
		BaseContext:       baseContext,
		ReadHeaderTimeout: metricsTimeout,
		ReadTimeout:       metricsTimeout,
		WriteTimeout:      metricsTimeout,
		IdleTimeout:       metricsIdleTimeout,
	}
	for _, listener := range []struct {
		name   string
		server *http.Server
	}{{"analytics", business}, {"metrics", metrics}} {
		group.Go(func() error {
			if err := listener.server.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
				return fmt.Errorf("serve %s HTTP: %w", listener.name, err)
			}
			return nil
		})
		group.Go(func() error {
			<-ctx.Done()
			shutdown, cancel := context.WithTimeout(context.WithoutCancel(ctx), shutdownTimeout)
			defer cancel()
			if err := listener.server.Shutdown(shutdown); err != nil {
				_ = listener.server.Close()
				return fmt.Errorf("shut down %s HTTP: %w", listener.name, err)
			}
			return nil
		})
	}

	logger.InfoContext(ctx, "starting analytics servers")
	if err := group.Wait(); err != nil {
		return err
	}
	logger.InfoContext(ctx, "analytics servers stopped")
	return nil
}

func resolveMetricsAddress(address string) (string, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil || host == "" || port == "" {
		return "", errors.New("ANALYTICS_METRICS_ADDRESS must include a specific host and port")
	}
	resolved, err := net.ResolveTCPAddr("tcp", address)
	if err != nil {
		return "", fmt.Errorf("resolve ANALYTICS_METRICS_ADDRESS: %w", err)
	}
	if resolved.IP == nil || resolved.IP.IsUnspecified() {
		return "", errors.New("ANALYTICS_METRICS_ADDRESS cannot use a wildcard address")
	}
	return resolved.String(), nil
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
