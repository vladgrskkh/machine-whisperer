package exporter

import (
	"errors"
	"net/url"
	"strings"
	"time"

	"github.com/caarlos0/env/v11"

	"github.com/vladgrskkh/machine-whisperer/api/internal/usecase/telemetry"
)

type Config struct {
	DatabaseURL     string        `env:"DATABASE_URL"`
	AnalyticsURL    string        `env:"ANALYTICS_URL"`
	AnalyticsAPIKey string        `env:"ANALYTICS_INGEST_API_KEY"`
	HTTPTimeout     time.Duration `env:"OUTBOX_HTTP_TIMEOUT"      envDefault:"30s"`
	Outbox          OutboxConfig
}

type OutboxConfig struct {
	BatchSize    int           `env:"OUTBOX_BATCH_SIZE"    envDefault:"1000"`
	PollInterval time.Duration `env:"OUTBOX_POLL_INTERVAL" envDefault:"1s"`
	Lease        time.Duration `env:"OUTBOX_LEASE"         envDefault:"2m"`
	RetryBase    time.Duration `env:"OUTBOX_RETRY_BASE"    envDefault:"1s"`
	RetryMax     time.Duration `env:"OUTBOX_RETRY_MAX"     envDefault:"5m"`
	Retention    time.Duration `env:"OUTBOX_RETENTION"     envDefault:"720h"`
}

const day = 24 * time.Hour

func LoadConfig() (Config, error) {
	var config Config
	if err := env.Parse(&config); err != nil {
		return Config{}, errors.New("invalid outbox relay environment configuration")
	}
	if err := config.validate(); err != nil {
		return Config{}, err
	}
	return config, nil
}

func (c Config) validate() error {
	if strings.TrimSpace(c.DatabaseURL) == "" {
		return errors.New("DATABASE_URL is required")
	}
	parsed, err := url.Parse(c.AnalyticsURL)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Hostname() == "" ||
		parsed.User != nil || parsed.RawQuery != "" || parsed.ForceQuery || strings.Contains(c.AnalyticsURL, "#") {
		return errors.New("ANALYTICS_URL must be an absolute HTTP or HTTPS base URL")
	}
	if c.AnalyticsAPIKey == "" || strings.ContainsFunc(c.AnalyticsAPIKey, func(r rune) bool {
		return r <= ' ' || r > '~'
	}) {
		return errors.New("ANALYTICS_INGEST_API_KEY must be a nonempty token")
	}
	if c.HTTPTimeout < time.Millisecond || c.HTTPTimeout >= c.Outbox.Lease {
		return errors.New("OUTBOX_HTTP_TIMEOUT must be at least 1ms and shorter than OUTBOX_LEASE")
	}
	return c.Outbox.validate()
}

func (c OutboxConfig) validate() error {
	const maxBatchSize = 1000
	if c.BatchSize < 1 || c.BatchSize > maxBatchSize {
		return errors.New("OUTBOX_BATCH_SIZE must be between 1 and 1000")
	}
	if c.PollInterval < time.Millisecond || c.PollInterval > day {
		return errors.New("OUTBOX_POLL_INTERVAL must be between 1ms and 24h")
	}
	if c.Lease < time.Millisecond || c.Lease > day {
		return errors.New("OUTBOX_LEASE must be between 1ms and 24h")
	}
	if c.RetryBase < time.Millisecond || c.RetryMax < c.RetryBase || c.RetryMax > day {
		return errors.New("OUTBOX_RETRY_BASE and OUTBOX_RETRY_MAX must be ordered and between 1ms and 24h")
	}
	if c.Retention < time.Millisecond {
		return errors.New("OUTBOX_RETENTION must be at least 1ms")
	}
	return nil
}

func (c OutboxConfig) Settings() telemetry.ExportSettings {
	return telemetry.ExportSettings{
		BatchSize:    c.BatchSize,
		PollInterval: c.PollInterval,
		Lease:        c.Lease,
		RetryBase:    c.RetryBase,
		RetryMax:     c.RetryMax,
		Retention:    c.Retention,
	}
}
