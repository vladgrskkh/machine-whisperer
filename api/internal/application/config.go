package application

import (
	"errors"
	"net/netip"
	"reflect"
	"strconv"
	"strings"
	"time"

	"github.com/caarlos0/env/v11"

	"github.com/vladgrskkh/machine-whisperer/api/internal/handler/middleware"
)

type Config struct {
	Database   DatabaseConfig
	Auth       AuthConfig
	Device     DeviceConfig
	RateLimits RateLimitsConfig
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

type environmentConfig struct {
	Database   databaseEnvironmentConfig
	Auth       authEnvironmentConfig
	Device     deviceEnvironmentConfig
	RateLimits rateLimitsEnvironmentConfig
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
	config := Config{
		Database: DatabaseConfig{URL: settings.Database.URL},
		Auth: AuthConfig{
			SigningKeyID:   settings.Auth.SigningKeyID,
			SigningKeyFile: settings.Auth.SigningKeyFile,
			VerifyKeysFile: settings.Auth.VerifyKeysFile,
		},
		Device: DeviceConfig{ClaimTTL: settings.Device.ClaimTTL},
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

func validateConfig(config Config) error {
	if config.Device.ClaimTTL <= 0 {
		return errInvalidClaimTTL
	}
	if config.Database.URL == "" {
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
	return nil
}
