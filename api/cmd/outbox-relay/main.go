package main

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/vladgrskkh/machine-whisperer/api/internal/exporter"
	analyticshttp "github.com/vladgrskkh/machine-whisperer/api/internal/infrastructure/telemetry/analytics"
	telemetrypostgres "github.com/vladgrskkh/machine-whisperer/api/internal/infrastructure/telemetry/postgres"
	"github.com/vladgrskkh/machine-whisperer/api/internal/usecase/telemetry"
)

const databaseStartupTimeout = 10 * time.Second

func run(ctx context.Context, logger *slog.Logger) error {
	config, err := exporter.LoadConfig()
	if err != nil {
		return err
	}
	startupCtx, cancel := context.WithTimeout(ctx, databaseStartupTimeout)
	pool, err := pgxpool.New(startupCtx, config.DatabaseURL)
	if err != nil {
		cancel()
		return errors.New("invalid database configuration")
	}
	defer pool.Close()
	err = pool.Ping(startupCtx)
	cancel()
	if err != nil {
		return errors.New("database connection failed")
	}
	repository := telemetrypostgres.NewOutbox(pool)
	sender := analyticshttp.NewHTTPClient(config.AnalyticsURL, config.AnalyticsAPIKey, config.HTTPTimeout)
	worker := telemetry.NewExportWorker(repository, sender, config.Outbox.Settings(), logger)
	logger.InfoContext(ctx, "outbox relay started")
	worker.Run(ctx)
	return nil
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	logger := slog.New(slog.NewJSONHandler(os.Stderr, nil))
	err := run(ctx, logger)
	stop()
	if err != nil {
		logger.Error("outbox relay stopped", "error", err)
		os.Exit(1)
	}
}
