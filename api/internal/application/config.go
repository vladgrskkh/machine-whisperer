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
	"github.com/vladgrskkh/machine-whisperer/api/internal/infrastructure/auth/attempts"
)

type Config struct {
	SigningKeyID   string
	SigningKeyFile string
	VerifyKeysFile string
	EmailAttempts  attempts.Config
	Now            func() time.Time
	DatabaseURL    string
	RateLimit      middleware.RateLimitConfig
}

type environmentConfig struct {
	DatabaseURL       string        `env:"DATABASE_URL"`
	SigningKeyID      string        `env:"AUTH_SIGNING_KEY_ID"`
	SigningKeyFile    string        `env:"AUTH_SIGNING_KEY_FILE"`
	VerifyKeysFile    string        `env:"AUTH_VERIFY_KEYS_FILE"`
	SignupLimit       int           `env:"AUTH_RATE_LIMIT"                   envDefault:"30"`
	SignupWindow      time.Duration `env:"AUTH_RATE_LIMIT_WINDOW"            envDefault:"1m"`
	SignupMaxClients  int           `env:"AUTH_RATE_LIMIT_MAX_CLIENTS"       envDefault:"10000"`
	EmailLimit        int           `env:"AUTH_EMAIL_RATE_LIMIT"             envDefault:"5"`
	EmailWindow       time.Duration `env:"AUTH_EMAIL_RATE_LIMIT_WINDOW"      envDefault:"1m"`
	EmailMaxClients   int           `env:"AUTH_EMAIL_RATE_LIMIT_MAX_CLIENTS" envDefault:"10000"`
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
		DatabaseURL:    settings.DatabaseURL,
		SigningKeyID:   settings.SigningKeyID,
		SigningKeyFile: settings.SigningKeyFile,
		VerifyKeysFile: settings.VerifyKeysFile,
		EmailAttempts: attempts.Config{
			Limit:    settings.EmailLimit,
			Window:   settings.EmailWindow,
			Capacity: settings.EmailMaxClients,
		},
		RateLimit: middleware.RateLimitConfig{
			Limit:      settings.SignupLimit,
			Window:     settings.SignupWindow,
			MaxClients: settings.SignupMaxClients,
		},
	}
	if err := validateConfig(config); err != nil {
		return Config{}, err
	}
	if value := settings.TrustedProxyCIDRs; value != "" {
		for entry := range strings.SplitSeq(value, ",") {
			prefix, parseErr := netip.ParsePrefix(strings.TrimSpace(entry))
			if parseErr != nil {
				return Config{}, errors.New("TRUSTED_PROXY_CIDRS must contain valid CIDRs")
			}
			config.RateLimit.TrustedProxies = append(config.RateLimit.TrustedProxies, prefix.Masked())
		}
	}
	return config, nil
}

func validateConfig(config Config) error {
	if config.DatabaseURL == "" {
		return errors.New("DATABASE_URL is required")
	}
	if config.RateLimit.Limit <= 0 {
		return errors.New("AUTH_RATE_LIMIT must be a positive integer")
	}
	if config.RateLimit.MaxClients <= 0 {
		return errors.New("AUTH_RATE_LIMIT_MAX_CLIENTS must be a positive integer")
	}
	if config.RateLimit.Window <= 0 {
		return errors.New("AUTH_RATE_LIMIT_WINDOW must be a positive duration")
	}
	if config.SigningKeyID == "" {
		return errors.New("AUTH_SIGNING_KEY_ID is required")
	}
	if config.SigningKeyFile == "" {
		return errors.New("AUTH_SIGNING_KEY_FILE is required")
	}
	if config.VerifyKeysFile == "" {
		return errors.New("AUTH_VERIFY_KEYS_FILE is required")
	}
	if config.EmailAttempts.Limit <= 0 {
		return errors.New("AUTH_EMAIL_RATE_LIMIT must be a positive integer")
	}
	if config.EmailAttempts.Window <= 0 {
		return errors.New("AUTH_EMAIL_RATE_LIMIT_WINDOW must be a positive duration")
	}
	if config.EmailAttempts.Capacity <= 0 {
		return errors.New("AUTH_EMAIL_RATE_LIMIT_MAX_CLIENTS must be a positive integer")
	}
	return nil
}
