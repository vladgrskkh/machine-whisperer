package main

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/vladgrskkh/machine-whisperer/api/internal/application"
)

const (
	readHeaderTimeout  = 5 * time.Second
	readTimeout        = 10 * time.Second
	shutdownTimeout    = 10 * time.Second
	metricsTimeout     = 5 * time.Second
	metricsIdleTimeout = 30 * time.Second
)

type metricsEndpoint struct {
	address string
	handler http.Handler
}

func run(
	ctx context.Context,
	address string,
	handler http.Handler,
	runWorkers func(context.Context),
	endpoint metricsEndpoint,
) error {
	certificate, err := loadTLSCertificate()
	if err != nil {
		return err
	}
	listener, err := (&net.ListenConfig{}).Listen(ctx, "tcp", address)
	if err != nil {
		return fmt.Errorf("listen: %w", err)
	}
	defer func() { _ = listener.Close() }()
	metricsListener, err := listenMetrics(ctx, endpoint.address)
	if err != nil {
		return fmt.Errorf("listen for metrics: %w", err)
	}
	defer func() { _ = metricsListener.Close() }()
	server := &http.Server{
		Handler: handler, ReadHeaderTimeout: readHeaderTimeout, ReadTimeout: readTimeout,
	}
	if len(certificate.Certificate) != 0 {
		server.TLSConfig = &tls.Config{Certificates: []tls.Certificate{certificate}, MinVersion: tls.VersionTLS12}
	}
	metrics := &http.Server{
		Handler: endpoint.handler, ReadHeaderTimeout: metricsTimeout, ReadTimeout: metricsTimeout,
		WriteTimeout: metricsTimeout, IdleTimeout: metricsIdleTimeout,
	}

	ctx, cancel := context.WithCancel(ctx)
	var background sync.WaitGroup
	background.Go(func() { runWorkers(ctx) })
	background.Go(func() { shutdownServers(ctx, server, metrics) })
	results := make(chan error)
	go func() {
		if server.TLSConfig != nil {
			results <- serveError("serve API HTTPS", server.ServeTLS(listener, "", ""))
			return
		}
		results <- serveError("serve API HTTP", server.Serve(listener))
	}()
	go func() {
		results <- serveError("serve metrics HTTP", metrics.Serve(metricsListener))
	}()
	firstErr := <-results
	cancel()
	background.Wait()
	return errors.Join(firstErr, <-results)
}

func loadTLSCertificate() (tls.Certificate, error) {
	certFile := os.Getenv("API_TLS_CERT_FILE")
	keyFile := os.Getenv("API_TLS_KEY_FILE")
	if (certFile == "") != (keyFile == "") {
		return tls.Certificate{}, errors.New("API_TLS_CERT_FILE and API_TLS_KEY_FILE must be set together")
	}
	if certFile == "" {
		return tls.Certificate{}, nil
	}
	certificate, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil {
		return tls.Certificate{}, errors.New("invalid API TLS certificate or key")
	}
	return certificate, nil
}

func listenMetrics(ctx context.Context, address string) (net.Listener, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil || host == "" || port == "" {
		return nil, errors.New("API_METRICS_ADDRESS must include a specific host and port")
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
		return nil, errors.New("API_METRICS_ADDRESS cannot use a wildcard address")
	}
	return (&net.ListenConfig{}).Listen(ctx, "tcp", net.JoinHostPort(ip.String(), port))
}

func shutdownServers(ctx context.Context, servers ...*http.Server) {
	<-ctx.Done()
	shutdownCtx, stop := context.WithTimeout(context.WithoutCancel(ctx), shutdownTimeout)
	defer stop()
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	var shutdowns sync.WaitGroup
	for _, server := range servers {
		shutdowns.Go(func() {
			if err := server.Shutdown(shutdownCtx); err != nil {
				logger.Error("shutdown failed", "error", err)
				_ = server.Close()
			}
		})
	}
	shutdowns.Wait()
}

func serveError(label string, err error) error {
	if err == nil || errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return fmt.Errorf("%s: %w", label, err)
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
	return run(ctx, address, app.Routes(), app.RunWorkers, metricsEndpoint{
		address: config.MetricsAddress,
		handler: app.MetricsHandler(),
	})
}

// @title Machine Whisperer API
// @version 1
// @description HTTP API for user accounts, device enrollment, machines, alerts and telemetry.
// @BasePath /
//
// @tag.name internal
// @tag.description Restricted manufacturing operations.
// @tag.name operational
// @tag.description Process health and API documentation.
//
// @securityDefinitions.apikey UserAccessJWT
// @in header
// @name Authorization
// @description Send one Authorization header with the complete value Bearer <access_token>.
//
// @securityDefinitions.apikey DeviceBearer
// @in header
// @name Authorization
// @description Send one Authorization header with the complete value Bearer <device_id.secret>.
// @description The device must be claimed. Authentication supplies the telemetry_writer role.
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
