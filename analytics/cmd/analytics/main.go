package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/vladgrskkh/machine-whisperer/analytics/internal/application"
	"github.com/vladgrskkh/machine-whisperer/analytics/internal/config"
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
	app, err := application.New(ctx, cfg, application.WithClickHouse(cfg.ClickHouse))
	if err != nil {
		return err
	}
	defer app.Close()
	return serveHTTP(ctx, logger, cfg, app.Routes(), app.MetricsHandler())
}

func serveHTTP(
	ctx context.Context, logger *slog.Logger, cfg config.Config, businessHandler, metricsHandler http.Handler,
) error {
	listener, err := (&net.ListenConfig{}).Listen(ctx, "tcp", cfg.Address)
	if err != nil {
		return fmt.Errorf("listen for analytics: %w", err)
	}
	defer func() { _ = listener.Close() }()
	metricsListener, err := listenMetrics(ctx, cfg.MetricsAddress)
	if err != nil {
		return fmt.Errorf("listen for metrics: %w", err)
	}
	defer func() { _ = metricsListener.Close() }()

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	baseContext := func(net.Listener) context.Context { return ctx }
	server := &http.Server{
		Handler:           businessHandler,
		BaseContext:       baseContext,
		ReadHeaderTimeout: headerTimeout,
		ReadTimeout:       readTimeout,
		WriteTimeout:      writeTimeout,
		IdleTimeout:       idleTimeout,
	}
	metrics := &http.Server{
		Handler:           metricsHandler,
		BaseContext:       baseContext,
		ReadHeaderTimeout: metricsTimeout,
		ReadTimeout:       metricsTimeout,
		WriteTimeout:      metricsTimeout,
		IdleTimeout:       metricsIdleTimeout,
	}
	const serverCount = 2
	results := make(chan error, serverCount)
	go func() { results <- serveServer("serve Analytics HTTP", server, listener) }()
	go func() { results <- serveServer("serve metrics HTTP", metrics, metricsListener) }()
	logger.InfoContext(ctx, "analytics server started")

	remaining := serverCount
	var serveErr error
	select {
	case serveErr = <-results:
		remaining--
	case <-ctx.Done():
	}
	cancel()
	shutdown, stop := context.WithTimeout(context.WithoutCancel(ctx), shutdownTimeout)
	defer stop()
	go func() { results <- shutdownServer(shutdown, "shut down Analytics HTTP", server) }()
	go func() { results <- shutdownServer(shutdown, "shut down metrics HTTP", metrics) }()
	for range remaining + serverCount {
		serveErr = errors.Join(serveErr, <-results)
	}
	logger.InfoContext(ctx, "analytics server stopped")
	return serveErr
}

func listenMetrics(ctx context.Context, address string) (net.Listener, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil || host == "" || port == "" {
		return nil, errors.New("ANALYTICS_METRICS_ADDRESS must include a specific host and port")
	}
	ip, err := netip.ParseAddr(host)
	if err != nil {
		resolved, lookupErr := net.DefaultResolver.LookupNetIP(ctx, "ip", host)
		if lookupErr != nil {
			return nil, fmt.Errorf("resolve metrics host: %w", lookupErr)
		}
		if len(resolved) == 0 {
			return nil, errors.New("metrics host has no IP address")
		}
		ip = resolved[0]
	}
	if ip.Unmap().WithZone("").IsUnspecified() {
		return nil, errors.New("ANALYTICS_METRICS_ADDRESS cannot use a wildcard address")
	}
	return (&net.ListenConfig{}).Listen(ctx, "tcp", net.JoinHostPort(ip.String(), port))
}

func serveServer(label string, server *http.Server, listener net.Listener) error {
	if err := server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("%s: %w", label, err)
	}
	return nil
}

func shutdownServer(ctx context.Context, label string, server *http.Server) error {
	if err := server.Shutdown(ctx); err != nil {
		_ = server.Close()
		return fmt.Errorf("%s: %w", label, err)
	}
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
