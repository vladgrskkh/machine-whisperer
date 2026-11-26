package main

import (
	"context"
	"crypto/tls"
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

	"github.com/vladgrskkh/machine-whisperer/api/internal/application"
	"github.com/vladgrskkh/machine-whisperer/api/internal/infrastructure/tracing"
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
	metricsAddress, err := resolveMetricsAddress(endpoint.address)
	if err != nil {
		return err
	}
	server := &http.Server{
		Addr: address, Handler: handler, ReadHeaderTimeout: readHeaderTimeout, ReadTimeout: readTimeout,
	}
	if len(certificate.Certificate) != 0 {
		server.TLSConfig = &tls.Config{Certificates: []tls.Certificate{certificate}, MinVersion: tls.VersionTLS12}
	}
	metrics := &http.Server{
		Addr:              metricsAddress,
		Handler:           endpoint.handler,
		ReadHeaderTimeout: metricsTimeout,
		ReadTimeout:       metricsTimeout,
		WriteTimeout:      metricsTimeout,
		IdleTimeout:       metricsIdleTimeout,
	}
	return runServers(ctx, server, metrics, runWorkers)
}

func runServers(ctx context.Context, apiServer, metricsServer *http.Server, runWorkers func(context.Context)) error {
	group, ctx := errgroup.WithContext(ctx)
	servers := []struct {
		name   string
		server *http.Server
	}{{"API", apiServer}, {"metrics", metricsServer}}
	ready := make(chan struct{}, len(servers))
	for _, entry := range servers {
		entry.server.BaseContext = func(net.Listener) context.Context {
			ready <- struct{}{}
			return context.WithoutCancel(ctx)
		}
		group.Go(func() error {
			serve := entry.server.ListenAndServe
			if entry.server.TLSConfig != nil {
				serve = func() error { return entry.server.ListenAndServeTLS("", "") }
			}
			if err := serve(); !errors.Is(err, http.ErrServerClosed) {
				return fmt.Errorf("serve %s: %w", entry.name, err)
			}
			return nil
		})
		group.Go(func() error {
			<-ctx.Done()
			shutdown, stop := context.WithTimeout(context.WithoutCancel(ctx), shutdownTimeout)
			defer stop()
			if err := entry.server.Shutdown(shutdown); err != nil {
				_ = entry.server.Close()
				return fmt.Errorf("shut down %s: %w", entry.name, err)
			}
			return nil
		})
	}
	group.Go(func() error {
		// Start the relay only after both HTTP servers have bound their addresses.
		for range servers {
			select {
			case <-ready:
			case <-ctx.Done():
				return nil
			}
		}
		if ctx.Err() == nil {
			runWorkers(ctx)
		}
		return nil
	})
	return group.Wait()
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

func resolveMetricsAddress(address string) (string, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil || host == "" || port == "" {
		return "", errors.New("API_METRICS_ADDRESS must include a specific host and port")
	}
	resolved, err := net.ResolveTCPAddr("tcp", address)
	if err != nil {
		return "", fmt.Errorf("resolve API_METRICS_ADDRESS: %w", err)
	}
	if resolved.IP == nil || resolved.IP.IsUnspecified() {
		return "", errors.New("API_METRICS_ADDRESS must bind to a specific IP address")
	}
	return resolved.String(), nil
}

func serve(ctx context.Context, address string) error {
	config, err := application.LoadConfig()
	if err != nil {
		return err
	}
	logger := slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug}))
	traces, err := tracing.New(ctx, "api")
	if err != nil {
		return err
	}
	defer func() {
		if traces.Shutdown(ctx) != nil {
			logger.WarnContext(ctx, "trace provider shutdown failed")
		}
	}()
	app, err := application.New(ctx, config,
		application.WithTracing(traces.Provider, traces.Propagator),
		application.WithDatabase(config.Database), application.WithLogger(logger),
	)
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
		slog.New(slog.NewJSONHandler(os.Stderr, nil)).Error("api stopped", "error", err)
		os.Exit(1)
	}
}
