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
	DatabaseURL string
	RateLimit   middleware.RateLimitConfig
}

type environmentConfig struct {
	DatabaseURL       string        `env:"DATABASE_URL"`
	SignupLimit       int           `env:"AUTH_RATE_LIMIT"             envDefault:"30"`
	SignupWindow      time.Duration `env:"AUTH_RATE_LIMIT_WINDOW"      envDefault:"1m"`
	SignupMaxClients  int           `env:"AUTH_RATE_LIMIT_MAX_CLIENTS" envDefault:"10000"`
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
		DatabaseURL: settings.DatabaseURL,
		RateLimit: middleware.RateLimitConfig{
			Limit:      settings.SignupLimit,
			Window:     settings.SignupWindow,
			MaxClients: settings.SignupMaxClients,
		},
	}
	if config.DatabaseURL == "" {
		return Config{}, errors.New("DATABASE_URL is required")
	}
	if config.RateLimit.Limit <= 0 {
		return Config{}, errors.New("AUTH_RATE_LIMIT must be a positive integer")
	}
	if config.RateLimit.MaxClients <= 0 {
		return Config{}, errors.New("AUTH_RATE_LIMIT_MAX_CLIENTS must be a positive integer")
	}
	if config.RateLimit.Window <= 0 {
		return Config{}, errors.New("AUTH_RATE_LIMIT_WINDOW must be a positive duration")
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
