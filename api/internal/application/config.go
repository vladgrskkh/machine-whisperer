package application

import (
	"errors"
	"fmt"
	"net/netip"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/vladgrskkh/machine-whisperer/api/internal/handler/middleware"
)

const (
	defaultSignupLimit    = 30
	defaultSignupCapacity = 10000
)

func LoadConfig() (Config, error) {
	config := Config{
		DatabaseURL: os.Getenv("DATABASE_URL"),
		RateLimit: middleware.RateLimitConfig{
			Limit:      defaultSignupLimit,
			Window:     time.Minute,
			MaxClients: defaultSignupCapacity,
		},
	}
	if config.DatabaseURL == "" {
		return Config{}, errors.New("DATABASE_URL is required")
	}
	limit, err := positiveIntEnv("AUTH_RATE_LIMIT", config.RateLimit.Limit)
	if err != nil {
		return Config{}, err
	}
	config.RateLimit.Limit = limit
	capacity, err := positiveIntEnv("AUTH_RATE_LIMIT_MAX_CLIENTS", config.RateLimit.MaxClients)
	if err != nil {
		return Config{}, err
	}
	config.RateLimit.MaxClients = capacity
	if value := os.Getenv("AUTH_RATE_LIMIT_WINDOW"); value != "" {
		window, parseErr := time.ParseDuration(value)
		if parseErr != nil || window <= 0 {
			return Config{}, errors.New("AUTH_RATE_LIMIT_WINDOW must be a positive duration")
		}
		config.RateLimit.Window = window
	}
	if value := os.Getenv("TRUSTED_PROXY_CIDRS"); value != "" {
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

func positiveIntEnv(name string, fallback int) (int, error) {
	value := os.Getenv(name)
	if value == "" {
		return fallback, nil
	}
	parsed, err := strconv.Atoi(value)
	if err != nil || parsed <= 0 {
		return 0, fmt.Errorf("%s must be a positive integer", name)
	}
	return parsed, nil
}
