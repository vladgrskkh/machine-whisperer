package application

import (
	"bytes"
	"errors"
	"math"
	"net/netip"
	"net/url"
	"os"
	"reflect"
	"strconv"
	"strings"
	"time"

	"github.com/caarlos0/env/v11"

	"github.com/vladgrskkh/machine-whisperer/api/internal/handler/middleware"
)

type Config struct {
	Database       DatabaseConfig
	Auth           AuthConfig
	Device         DeviceConfig
	RateLimits     RateLimitsConfig
	Relay          RelayConfig
	Detector       DetectorConfig
	MetricsAddress string
}

type DatabaseConfig struct {
	URL string
}

type AuthConfig struct {
	SigningKeyID   string
	SigningKeyFile string
	VerifyKeysFile string
	Now            func() time.Time
}

type DeviceConfig struct {
	ClaimTTL time.Duration
}

const defaultClaimTTL = 24 * time.Hour

var errInvalidClaimTTL = errors.New("DEVICE_CLAIM_TTL must be a positive duration")

type RateLimitsConfig struct {
	IP middleware.RateLimitConfig
}

type RelayConfig struct {
	AnalyticsURL    string        `env:"ANALYTICS_URL"`
	AnalyticsAPIKey string        `env:"ANALYTICS_INGEST_API_KEY"`
	HTTPTimeout     time.Duration `env:"OUTBOX_HTTP_TIMEOUT"      envDefault:"30s"`
	BatchSize       int           `env:"OUTBOX_BATCH_SIZE"        envDefault:"1000"`
	PollInterval    time.Duration `env:"OUTBOX_POLL_INTERVAL"     envDefault:"1s"`
	Lease           time.Duration `env:"OUTBOX_LEASE"             envDefault:"2m"`
	RetryBase       time.Duration `env:"OUTBOX_RETRY_BASE"        envDefault:"1s"`
	RetryMax        time.Duration `env:"OUTBOX_RETRY_MAX"         envDefault:"5m"`
	Retention       time.Duration `env:"OUTBOX_RETENTION"         envDefault:"720h"`
}

type DetectorConfig struct {
	AlertZThreshold               float64 `env:"DETECTOR_ALERT_Z_THRESHOLD"               envDefault:"3"`
	RecoveryZThreshold            float64 `env:"DETECTOR_RECOVERY_Z_THRESHOLD"            envDefault:"2"`
	AlertSustainedObservations    int     `env:"DETECTOR_ALERT_SUSTAINED_OBSERVATIONS"    envDefault:"3"`
	RecoverySustainedObservations int     `env:"DETECTOR_RECOVERY_SUSTAINED_OBSERVATIONS" envDefault:"3"`
}

const (
	day                          = 24 * time.Hour
	defaultAlertZThreshold       = 3
	defaultRecoveryZThreshold    = 2
	defaultSustainedObservations = 3
)

func (c DetectorConfig) withDefaults() DetectorConfig {
	if c != (DetectorConfig{}) {
		return c
	}
	return DetectorConfig{
		AlertZThreshold:               defaultAlertZThreshold,
		RecoveryZThreshold:            defaultRecoveryZThreshold,
		AlertSustainedObservations:    defaultSustainedObservations,
		RecoverySustainedObservations: defaultSustainedObservations,
	}
}

func (c DetectorConfig) validate() error {
	if !(c.RecoveryZThreshold >= 0 && c.AlertZThreshold > c.RecoveryZThreshold && c.AlertZThreshold < math.Inf(1)) {
		return errors.New("detector thresholds must be finite and satisfy " +
			"DETECTOR_ALERT_Z_THRESHOLD > DETECTOR_RECOVERY_Z_THRESHOLD >= 0")
	}
	if c.AlertSustainedObservations <= 0 || c.RecoverySustainedObservations <= 0 {
		return errors.New(
			"DETECTOR_ALERT_SUSTAINED_OBSERVATIONS and DETECTOR_RECOVERY_SUSTAINED_OBSERVATIONS must be positive",
		)
	}
	return nil
}

type environmentConfig struct {
	DatabaseURLFile     string `env:"DATABASE_URL_FILE"`
	AnalyticsAPIKeyFile string `env:"ANALYTICS_INGEST_API_KEY_FILE"`
	Database            databaseEnvironmentConfig
	Auth                authEnvironmentConfig
	Device              deviceEnvironmentConfig
	RateLimits          rateLimitsEnvironmentConfig
	Relay               RelayConfig
	Detector            DetectorConfig
	MetricsAddress      string `env:"API_METRICS_ADDRESS"           envDefault:"127.0.0.1:9091"`
}

type databaseEnvironmentConfig struct {
	URL string `env:"DATABASE_URL"`
}

type authEnvironmentConfig struct {
	SigningKeyID   string `env:"AUTH_SIGNING_KEY_ID"`
	SigningKeyFile string `env:"AUTH_SIGNING_KEY_FILE"`
	VerifyKeysFile string `env:"AUTH_VERIFY_KEYS_FILE"`
}

type deviceEnvironmentConfig struct {
	ClaimTTL time.Duration `env:"DEVICE_CLAIM_TTL" envDefault:"24h"`
}

type rateLimitsEnvironmentConfig struct {
	IPLimit           int           `env:"AUTH_RATE_LIMIT"             envDefault:"30"`
	IPWindow          time.Duration `env:"AUTH_RATE_LIMIT_WINDOW"      envDefault:"1m"`
	IPMaxClients      int           `env:"AUTH_RATE_LIMIT_MAX_CLIENTS" envDefault:"10000"`
	TrustedProxyCIDRs string        `env:"TRUSTED_PROXY_CIDRS"`
}

func LoadConfig() (Config, error) {
	var settings environmentConfig
	if err := env.ParseWithOptions(&settings, env.Options{
		FuncMap: map[reflect.Type]env.ParserFunc{
			// The default env int parser is limited to 32 bits on every platform.
			reflect.TypeFor[int](): func(value string) (any, error) { return strconv.Atoi(value) },
		},
	}); err != nil {
		// Parser errors include supplied values, which can contain secrets.
		return Config{}, errors.New("invalid environment configuration")
	}
	if err := loadSecretFile(&settings.Database.URL, settings.DatabaseURLFile, "DATABASE_URL_FILE"); err != nil {
		return Config{}, err
	}
	if err := loadSecretFile(
		&settings.Relay.AnalyticsAPIKey, settings.AnalyticsAPIKeyFile, "ANALYTICS_INGEST_API_KEY_FILE",
	); err != nil {
		return Config{}, err
	}
	config := Config{
		Database: DatabaseConfig{URL: settings.Database.URL},
		Auth: AuthConfig{
			SigningKeyID:   settings.Auth.SigningKeyID,
			SigningKeyFile: settings.Auth.SigningKeyFile,
			VerifyKeysFile: settings.Auth.VerifyKeysFile,
		},
		Device:         DeviceConfig{ClaimTTL: settings.Device.ClaimTTL},
		Relay:          settings.Relay,
		Detector:       settings.Detector,
		MetricsAddress: settings.MetricsAddress,
		RateLimits: RateLimitsConfig{
			IP: middleware.RateLimitConfig{
				Limit:      settings.RateLimits.IPLimit,
				Window:     settings.RateLimits.IPWindow,
				MaxClients: settings.RateLimits.IPMaxClients,
			},
		},
	}
	if err := validateConfig(config); err != nil {
		return Config{}, err
	}
	if value := settings.RateLimits.TrustedProxyCIDRs; value != "" {
		for entry := range strings.SplitSeq(value, ",") {
			prefix, parseErr := netip.ParsePrefix(strings.TrimSpace(entry))
			if parseErr != nil {
				return Config{}, errors.New("TRUSTED_PROXY_CIDRS must contain valid CIDRs")
			}
			config.RateLimits.IP.TrustedProxies = append(config.RateLimits.IP.TrustedProxies, prefix.Masked())
		}
	}
	return config, nil
}

func loadSecretFile(value *string, path, variable string) error {
	if path == "" {
		return nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return errors.New("cannot read " + variable)
	}
	if bytes.IndexByte(data, 0) >= 0 {
		return errors.New(variable + " must not contain NUL bytes")
	}
	*value = strings.TrimRight(string(data), "\r\n")
	return nil
}

func validateConfig(config Config) error {
	if config.Device.ClaimTTL <= 0 {
		return errInvalidClaimTTL
	}
	if strings.TrimSpace(config.Database.URL) == "" {
		return errors.New("DATABASE_URL is required")
	}
	if config.RateLimits.IP.Limit <= 0 {
		return errors.New("AUTH_RATE_LIMIT must be a positive integer")
	}
	if config.RateLimits.IP.MaxClients <= 0 {
		return errors.New("AUTH_RATE_LIMIT_MAX_CLIENTS must be a positive integer")
	}
	if config.RateLimits.IP.Window <= 0 {
		return errors.New("AUTH_RATE_LIMIT_WINDOW must be a positive duration")
	}
	if config.Auth.SigningKeyID == "" {
		return errors.New("AUTH_SIGNING_KEY_ID is required")
	}
	if config.Auth.SigningKeyFile == "" {
		return errors.New("AUTH_SIGNING_KEY_FILE is required")
	}
	if config.Auth.VerifyKeysFile == "" {
		return errors.New("AUTH_VERIFY_KEYS_FILE is required")
	}
	if err := config.Detector.validate(); err != nil {
		return err
	}
	return config.Relay.validate()
}

func (c RelayConfig) validate() error {
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
	if c.HTTPTimeout < time.Millisecond || c.HTTPTimeout >= c.Lease {
		return errors.New("OUTBOX_HTTP_TIMEOUT must be at least 1ms and shorter than OUTBOX_LEASE")
	}
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
