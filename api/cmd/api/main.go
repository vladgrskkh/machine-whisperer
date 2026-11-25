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
	certFile := os.Getenv("API_TLS_CERT_FILE")
	keyFile := os.Getenv("API_TLS_KEY_FILE")
	if (certFile == "") != (keyFile == "") {
		return errors.New("API_TLS_CERT_FILE and API_TLS_KEY_FILE must be set together")
	}
	var tlsConfig *tls.Config
	if certFile != "" {
		certificate, err := tls.LoadX509KeyPair(certFile, keyFile)
		if err != nil {
			return errors.New("invalid API TLS certificate or key")
		}
		tlsConfig = &tls.Config{Certificates: []tls.Certificate{certificate}, MinVersion: tls.VersionTLS12}
	}

	listener, err := (&net.ListenConfig{}).Listen(ctx, "tcp", address)
	if err != nil {
		return fmt.Errorf("listen: %w", err)
	}
	server := &http.Server{
		Handler: handler, ReadHeaderTimeout: readHeaderTimeout, ReadTimeout: readTimeout, TLSConfig: tlsConfig,
	}
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
	if tlsConfig != nil {
		err = server.ServeTLS(listener, "", "")
	} else {
		err = server.Serve(listener)
	}
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
