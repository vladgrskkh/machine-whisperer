package main

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/vladgrskkh/machine-whisperer/analytics/internal/config"
	"github.com/vladgrskkh/machine-whisperer/analytics/internal/infrastructure/clickhouse"
	"github.com/vladgrskkh/machine-whisperer/analytics/migrations"
)

const migrationTimeout = time.Minute

func run(ctx context.Context) error {
	cfg, err := config.LoadClickHouse()
	if err != nil {
		return err
	}
	conn, err := clickhouse.Open(ctx, cfg)
	if err != nil {
		return err
	}
	defer conn.Close()
	ctx, cancel := context.WithTimeout(ctx, migrationTimeout)
	defer cancel()
	if err = conn.Exec(ctx, migrations.CreateTelemetryEvents); err != nil {
		return errors.New("ClickHouse telemetry migration failed")
	}
	return nil
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	logger := slog.New(slog.NewJSONHandler(os.Stderr, nil))
	err := run(ctx)
	stop()
	if err != nil {
		logger.Error("analytics migration stopped", "error", err)
		os.Exit(1)
	}
	logger.Info("analytics migration applied")
}
