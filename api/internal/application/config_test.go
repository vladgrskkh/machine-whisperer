package application_test

import (
	"net/netip"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vladgrskkh/machine-whisperer/api/internal/application"
)

func TestLoadConfig(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://signup:test@localhost/signup")
	t.Setenv("AUTH_RATE_LIMIT", "")
	t.Setenv("AUTH_RATE_LIMIT_WINDOW", "")
	t.Setenv("AUTH_RATE_LIMIT_MAX_CLIENTS", "")
	t.Setenv("TRUSTED_PROXY_CIDRS", "")
	config, err := application.LoadConfig()
	require.NoError(t, err)
	assert.Equal(t, 30, config.RateLimit.Limit)
	assert.Equal(t, time.Minute, config.RateLimit.Window)
	assert.Equal(t, 10000, config.RateLimit.MaxClients)
	assert.Empty(t, config.RateLimit.TrustedProxies)
	t.Setenv("AUTH_RATE_LIMIT", "2")
	t.Setenv("AUTH_RATE_LIMIT_WINDOW", "30s")
	t.Setenv("AUTH_RATE_LIMIT_MAX_CLIENTS", "20")
	t.Setenv("TRUSTED_PROXY_CIDRS", "127.0.0.0/8, ::1/128")
	config, err = application.LoadConfig()
	require.NoError(t, err)
	assert.Equal(t, 2, config.RateLimit.Limit)
	assert.Equal(t, 30*time.Second, config.RateLimit.Window)
	assert.Equal(t, 20, config.RateLimit.MaxClients)
	assert.Equal(
		t,
		[]netip.Prefix{netip.MustParsePrefix("127.0.0.0/8"), netip.MustParsePrefix("::1/128")},
		config.RateLimit.TrustedProxies,
	)
}

func TestLoadConfigRejectsInvalidSettings(t *testing.T) {
	for _, tc := range []struct{ key, value string }{
		{"DATABASE_URL", ""}, {"AUTH_RATE_LIMIT", "0"}, {"AUTH_RATE_LIMIT", "not a number"},
		{"AUTH_RATE_LIMIT_WINDOW", "-1s"}, {"AUTH_RATE_LIMIT_WINDOW", "unknown"},
		{"AUTH_RATE_LIMIT_MAX_CLIENTS", "-1"}, {"TRUSTED_PROXY_CIDRS", "127.0.0.1"},
	} {
		t.Run(tc.key+tc.value, func(t *testing.T) {
			t.Setenv("DATABASE_URL", "postgres://signup:test@localhost/signup")
			t.Setenv("AUTH_RATE_LIMIT", "")
			t.Setenv("AUTH_RATE_LIMIT_WINDOW", "")
			t.Setenv("AUTH_RATE_LIMIT_MAX_CLIENTS", "")
			t.Setenv("TRUSTED_PROXY_CIDRS", "")
			t.Setenv(tc.key, tc.value)
			_, err := application.LoadConfig()
			assert.Error(t, err)
		})
	}
}
