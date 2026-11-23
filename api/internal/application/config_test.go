package application_test

import (
	"math"
	"net/netip"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vladgrskkh/machine-whisperer/api/internal/application"
)

func TestLoadConfig(t *testing.T) {
	configEnvironment(t)
	t.Setenv("DATABASE_URL", "postgres://signup:test@localhost/signup")
	t.Setenv("AUTH_RATE_LIMIT", "")
	t.Setenv("AUTH_RATE_LIMIT_WINDOW", "")
	t.Setenv("AUTH_RATE_LIMIT_MAX_CLIENTS", "")
	t.Setenv("TRUSTED_PROXY_CIDRS", "")
	config, err := application.LoadConfig()
	require.NoError(t, err)
	assert.Equal(t, 30, config.RateLimits.IP.Limit)
	assert.Equal(t, time.Minute, config.RateLimits.IP.Window)
	assert.Equal(t, 10000, config.RateLimits.IP.MaxClients)
	assert.Empty(t, config.RateLimits.IP.TrustedProxies)
	t.Setenv("AUTH_RATE_LIMIT", "2")
	t.Setenv("AUTH_RATE_LIMIT_WINDOW", "30s")
	t.Setenv("AUTH_RATE_LIMIT_MAX_CLIENTS", "20")
	t.Setenv("TRUSTED_PROXY_CIDRS", "127.0.0.0/8, ::1/128")
	config, err = application.LoadConfig()
	require.NoError(t, err)
	assert.Equal(t, 2, config.RateLimits.IP.Limit)
	assert.Equal(t, 30*time.Second, config.RateLimits.IP.Window)
	assert.Equal(t, 20, config.RateLimits.IP.MaxClients)
	assert.Equal(
		t,
		[]netip.Prefix{netip.MustParsePrefix("127.0.0.0/8"), netip.MustParsePrefix("::1/128")},
		config.RateLimits.IP.TrustedProxies,
	)
}

func TestLoadConfigRejectsInvalidSettings(t *testing.T) {
	for _, tc := range []struct{ key, value string }{
		{"DATABASE_URL", ""}, {"AUTH_RATE_LIMIT", "0"}, {"AUTH_RATE_LIMIT", "not a number"},
		{"AUTH_RATE_LIMIT_WINDOW", "-1s"}, {"AUTH_RATE_LIMIT_WINDOW", "unknown"},
		{"AUTH_RATE_LIMIT_MAX_CLIENTS", "-1"}, {"TRUSTED_PROXY_CIDRS", "127.0.0.1"},
	} {
		t.Run(tc.key+tc.value, func(t *testing.T) {
			configEnvironment(t)
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

func TestLoadConfigErrorsDoNotExposeEnvironmentValues(t *testing.T) {
	const databaseURL = "postgres://signup:database-secret@localhost/signup"
	for _, key := range []string{
		"AUTH_RATE_LIMIT", "AUTH_RATE_LIMIT_WINDOW", "AUTH_RATE_LIMIT_MAX_CLIENTS", "TRUSTED_PROXY_CIDRS",
	} {
		t.Run(key, func(t *testing.T) {
			configEnvironment(t)
			t.Setenv("DATABASE_URL", databaseURL)
			t.Setenv("AUTH_RATE_LIMIT", "")
			t.Setenv("AUTH_RATE_LIMIT_WINDOW", "")
			t.Setenv("AUTH_RATE_LIMIT_MAX_CLIENTS", "")
			t.Setenv("TRUSTED_PROXY_CIDRS", "")
			t.Setenv(key, "private-invalid-value")
			config, err := application.LoadConfig()
			require.Error(t, err)
			assert.Zero(t, config)
			assert.NotContains(t, err.Error(), "private-invalid-value")
			assert.NotContains(t, err.Error(), databaseURL)
			assert.NotContains(t, err.Error(), "database-secret")
		})
	}
}

func TestLoadConfigAcceptsPlatformSizedIntegers(t *testing.T) {
	configEnvironment(t)
	t.Setenv("DATABASE_URL", "postgres://signup:test@localhost/signup")
	t.Setenv("AUTH_RATE_LIMIT", strconv.Itoa(math.MaxInt))
	t.Setenv("AUTH_RATE_LIMIT_WINDOW", "")
	t.Setenv("AUTH_RATE_LIMIT_MAX_CLIENTS", strconv.Itoa(math.MaxInt))
	t.Setenv("TRUSTED_PROXY_CIDRS", "")
	config, err := application.LoadConfig()
	require.NoError(t, err)
	assert.Equal(t, math.MaxInt, config.RateLimits.IP.Limit)
	assert.Equal(t, math.MaxInt, config.RateLimits.IP.MaxClients)
}

func configEnvironment(t *testing.T) {
	t.Helper()
	t.Setenv("AUTH_SIGNING_KEY_ID", "active")
	t.Setenv("AUTH_SIGNING_KEY_FILE", "/test/private.pem")
	t.Setenv("AUTH_VERIFY_KEYS_FILE", "/test/public.json")
	t.Setenv("ANALYTICS_URL", "http://localhost:8081")
	t.Setenv("ANALYTICS_INGEST_API_KEY", "test-key")
	t.Setenv("OUTBOX_HTTP_TIMEOUT", "")
	t.Setenv("OUTBOX_BATCH_SIZE", "")
	t.Setenv("OUTBOX_POLL_INTERVAL", "")
	t.Setenv("OUTBOX_LEASE", "")
	t.Setenv("OUTBOX_RETRY_BASE", "")
	t.Setenv("OUTBOX_RETRY_MAX", "")
	t.Setenv("OUTBOX_RETENTION", "")
}

func TestAuthConfig(t *testing.T) {
	configEnvironment(t)
	t.Setenv("DATABASE_URL", "postgres://test")
	config, err := application.LoadConfig()
	require.NoError(t, err)
	assert.Equal(t, "active", config.Auth.SigningKeyID)
	assert.Equal(t, "/test/private.pem", config.Auth.SigningKeyFile)
	assert.Equal(t, "/test/public.json", config.Auth.VerifyKeysFile)
}

func TestAuthConfigRejectsInvalidSettings(t *testing.T) {
	for _, tc := range []struct{ key, value string }{
		{"AUTH_SIGNING_KEY_ID", ""}, {"AUTH_SIGNING_KEY_FILE", ""}, {"AUTH_VERIFY_KEYS_FILE", ""},
	} {
		t.Run(tc.key+tc.value, func(t *testing.T) {
			configEnvironment(t)
			t.Setenv("DATABASE_URL", "postgres://secret")
			t.Setenv(tc.key, tc.value)
			config, err := application.LoadConfig()
			require.Error(t, err)
			assert.Zero(t, config)
			assert.NotContains(t, err.Error(), "private-invalid-value")
			assert.NotContains(t, err.Error(), "postgres://secret")
		})
	}
}

func TestDeviceClaimTTLConfiguration(t *testing.T) {
	configEnvironment(t)
	t.Setenv("DATABASE_URL", "postgres://test")
	t.Setenv("DEVICE_CLAIM_TTL", "")
	config, err := application.LoadConfig()
	require.NoError(t, err)
	assert.Equal(t, 24*time.Hour, config.Device.ClaimTTL)

	t.Setenv("DEVICE_CLAIM_TTL", "45m")
	config, err = application.LoadConfig()
	require.NoError(t, err)
	assert.Equal(t, 45*time.Minute, config.Device.ClaimTTL)

	for _, value := range []string{"0", "-1m", "private-invalid-claim-ttl"} {
		t.Setenv("DEVICE_CLAIM_TTL", value)
		config, err = application.LoadConfig()
		require.Error(t, err)
		assert.Zero(t, config)
		assert.NotContains(t, err.Error(), value)
	}
}

func TestRelayConfigDefaults(t *testing.T) {
	configEnvironment(t)
	t.Setenv("DATABASE_URL", "postgres://test")
	config, err := application.LoadConfig()
	require.NoError(t, err)
	assert.Equal(t, application.RelayConfig{
		AnalyticsURL: "http://localhost:8081", AnalyticsAPIKey: "test-key",
		HTTPTimeout: 30 * time.Second, BatchSize: 1000, PollInterval: time.Second,
		Lease: 2 * time.Minute, RetryBase: time.Second, RetryMax: 5 * time.Minute,
		Retention: 30 * 24 * time.Hour,
	}, config.Relay)
}

func TestRelayConfigRejectsInvalidSettings(t *testing.T) {
	for _, tc := range []struct{ key, value string }{
		{"ANALYTICS_URL", ""},
		{"ANALYTICS_URL", "http://user:password@example.com"},
		{"ANALYTICS_INGEST_API_KEY", ""},
		{"ANALYTICS_INGEST_API_KEY", "private invalid token"},
		{"OUTBOX_HTTP_TIMEOUT", "2m"},
		{"OUTBOX_BATCH_SIZE", "1001"},
		{"OUTBOX_POLL_INTERVAL", "0s"},
		{"OUTBOX_LEASE", "25h"},
		{"OUTBOX_RETRY_BASE", "6m"},
		{"OUTBOX_RETRY_MAX", "0s"},
		{"OUTBOX_RETENTION", "999us"},
	} {
		t.Run(tc.key+"/"+tc.value, func(t *testing.T) {
			configEnvironment(t)
			t.Setenv("DATABASE_URL", "postgres://test")
			t.Setenv(tc.key, tc.value)
			config, err := application.LoadConfig()
			require.Error(t, err)
			assert.Zero(t, config)
			if tc.key == "ANALYTICS_URL" && tc.value != "" || tc.key == "ANALYTICS_INGEST_API_KEY" && tc.value != "" {
				assert.NotContains(t, err.Error(), tc.value)
			}
		})
	}
}
