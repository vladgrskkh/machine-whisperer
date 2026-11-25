package application_test

import (
	"math"
	"net/netip"
	"os"
	"path/filepath"
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
	config, err := application.LoadConfig()
	require.NoError(t, err)
	assert.Equal(t, 30, config.RateLimits.IP.Limit)
	assert.Equal(t, time.Minute, config.RateLimits.IP.Window)
	assert.Equal(t, 10000, config.RateLimits.IP.MaxClients)
	assert.Empty(t, config.RateLimits.IP.TrustedProxies)
	assert.Equal(t, "127.0.0.1:9091", config.MetricsAddress)
	t.Setenv("AUTH_RATE_LIMIT", "2")
	t.Setenv("AUTH_RATE_LIMIT_WINDOW", "30s")
	t.Setenv("AUTH_RATE_LIMIT_MAX_CLIENTS", "20")
	t.Setenv("TRUSTED_PROXY_CIDRS", "127.0.0.0/8, ::1/128")
	t.Setenv("API_METRICS_ADDRESS", "api-metrics:9091")
	config, err = application.LoadConfig()
	require.NoError(t, err)
	assert.Equal(t, 2, config.RateLimits.IP.Limit)
	assert.Equal(t, 30*time.Second, config.RateLimits.IP.Window)
	assert.Equal(t, 20, config.RateLimits.IP.MaxClients)
	assert.Equal(t, "api-metrics:9091", config.MetricsAddress)
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
	t.Setenv("AUTH_RATE_LIMIT_MAX_CLIENTS", strconv.Itoa(math.MaxInt))
	config, err := application.LoadConfig()
	require.NoError(t, err)
	assert.Equal(t, math.MaxInt, config.RateLimits.IP.Limit)
	assert.Equal(t, math.MaxInt, config.RateLimits.IP.MaxClients)
}

func configEnvironment(t *testing.T) {
	t.Helper()
	t.Setenv("API_METRICS_ADDRESS", "")
	t.Setenv("DATABASE_URL_FILE", "")
	t.Setenv("ANALYTICS_INGEST_API_KEY_FILE", "")
	t.Setenv("AUTH_SIGNING_KEY_ID", "active")
	t.Setenv("AUTH_SIGNING_KEY_FILE", "/test/private.pem")
	t.Setenv("AUTH_VERIFY_KEYS_FILE", "/test/public.json")
	t.Setenv("AUTH_RATE_LIMIT", "")
	t.Setenv("AUTH_RATE_LIMIT_WINDOW", "")
	t.Setenv("AUTH_RATE_LIMIT_MAX_CLIENTS", "")
	t.Setenv("TRUSTED_PROXY_CIDRS", "")
	t.Setenv("DEVICE_CLAIM_TTL", "")
	t.Setenv("ANALYTICS_URL", "http://localhost:8081")
	t.Setenv("ANALYTICS_INGEST_API_KEY", "test-key")
	t.Setenv("OUTBOX_HTTP_TIMEOUT", "")
	t.Setenv("OUTBOX_BATCH_SIZE", "")
	t.Setenv("OUTBOX_POLL_INTERVAL", "")
	t.Setenv("OUTBOX_LEASE", "")
	t.Setenv("OUTBOX_RETRY_BASE", "")
	t.Setenv("OUTBOX_RETRY_MAX", "")
	t.Setenv("OUTBOX_RETENTION", "")
	t.Setenv("DETECTOR_ALERT_Z_THRESHOLD", "")
	t.Setenv("DETECTOR_RECOVERY_Z_THRESHOLD", "")
	t.Setenv("DETECTOR_ALERT_SUSTAINED_OBSERVATIONS", "")
	t.Setenv("DETECTOR_RECOVERY_SUSTAINED_OBSERVATIONS", "")
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
	for _, key := range []string{
		"DEVICE_CLAIM_TTL", "AUTH_RATE_LIMIT", "AUTH_RATE_LIMIT_WINDOW", "AUTH_RATE_LIMIT_MAX_CLIENTS",
		"TRUSTED_PROXY_CIDRS",
	} {
		t.Setenv(key, "invalid")
	}
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

func TestDetectorConfigDefaultsAndOverrides(t *testing.T) {
	configEnvironment(t)
	t.Setenv("DATABASE_URL", "postgres://test")
	defaults, err := application.LoadConfig()
	require.NoError(t, err)
	assert.Equal(t, application.DetectorConfig{
		AlertZThreshold: 3, RecoveryZThreshold: 2, AlertSustainedObservations: 3, RecoverySustainedObservations: 3,
	}, defaults.Detector)
	t.Setenv("DETECTOR_ALERT_Z_THRESHOLD", "4.5")
	t.Setenv("DETECTOR_RECOVERY_Z_THRESHOLD", "0")
	t.Setenv("DETECTOR_ALERT_SUSTAINED_OBSERVATIONS", "1")
	t.Setenv("DETECTOR_RECOVERY_SUSTAINED_OBSERVATIONS", "5")
	overrides, err := application.LoadConfig()
	require.NoError(t, err)
	assert.Equal(t, application.DetectorConfig{
		AlertZThreshold: 4.5, RecoveryZThreshold: 0, AlertSustainedObservations: 1, RecoverySustainedObservations: 5,
	}, overrides.Detector)
}

func TestDetectorConfigRejectsInvalidSettings(t *testing.T) {
	for _, tc := range []struct{ key, value string }{
		{"DETECTOR_ALERT_Z_THRESHOLD", "2"},
		{"DETECTOR_ALERT_Z_THRESHOLD", "NaN"},
		{"DETECTOR_ALERT_Z_THRESHOLD", "+Inf"},
		{"DETECTOR_RECOVERY_Z_THRESHOLD", "-0.1"},
		{"DETECTOR_RECOVERY_Z_THRESHOLD", "4"},
		{"DETECTOR_ALERT_SUSTAINED_OBSERVATIONS", "0"},
		{"DETECTOR_RECOVERY_SUSTAINED_OBSERVATIONS", "-1"},
	} {
		t.Run(tc.key+"/"+tc.value, func(t *testing.T) {
			configEnvironment(t)
			t.Setenv("DATABASE_URL", "postgres://test")
			t.Setenv(tc.key, tc.value)
			config, err := application.LoadConfig()
			require.Error(t, err)
			assert.Zero(t, config)
		})
	}
}

func TestLoadConfigReadsSecretsFromFiles(t *testing.T) {
	configEnvironment(t)
	t.Setenv("DATABASE_URL", "ignored")
	t.Setenv("ANALYTICS_INGEST_API_KEY", "ignored")
	dir := t.TempDir()
	databaseFile := filepath.Join(dir, "database")
	tokenFile := filepath.Join(dir, "token")
	const databaseURL = " postgres://signup:database-secret@localhost/signup "
	require.NoError(t, os.WriteFile(databaseFile, []byte(databaseURL+"\r\n"), 0o600))
	require.NoError(t, os.WriteFile(tokenFile, []byte("private-token\n"), 0o600))
	t.Setenv("DATABASE_URL_FILE", databaseFile)
	t.Setenv("ANALYTICS_INGEST_API_KEY_FILE", tokenFile)

	config, err := application.LoadConfig()
	require.NoError(t, err)
	assert.Equal(t, databaseURL, config.Database.URL)
	assert.Equal(t, "private-token", config.Relay.AnalyticsAPIKey)
}

func TestLoadConfigRejectsUnreadableSecretFiles(t *testing.T) {
	for _, variable := range []string{"DATABASE_URL_FILE", "ANALYTICS_INGEST_API_KEY_FILE"} {
		t.Run(variable, func(t *testing.T) {
			configEnvironment(t)
			t.Setenv("DATABASE_URL", "postgres://signup:test@localhost/signup")
			t.Setenv(variable, filepath.Join(t.TempDir(), "private-secret-path"))
			config, err := application.LoadConfig()
			require.Error(t, err)
			assert.Zero(t, config)
			assert.NotContains(t, err.Error(), "private-secret-path")
		})
	}
}

func TestLoadConfigRejectsInvalidTokenFileWithoutLeakingContents(t *testing.T) {
	configEnvironment(t)
	t.Setenv("DATABASE_URL", "postgres://signup:test@localhost/signup")
	tokenFile := filepath.Join(t.TempDir(), "token")
	require.NoError(t, os.WriteFile(tokenFile, []byte(" private-token\n"), 0o600))
	t.Setenv("ANALYTICS_INGEST_API_KEY_FILE", tokenFile)

	config, err := application.LoadConfig()
	require.Error(t, err)
	assert.Zero(t, config)
	assert.NotContains(t, err.Error(), "private-token")
}

func TestLoadConfigRejectsDatabaseURLFileWithNUL(t *testing.T) {
	configEnvironment(t)
	path := filepath.Join(t.TempDir(), "private-database-url")
	require.NoError(t, os.WriteFile(path, []byte("postgres://user:private-fixture\x00tail@localhost/db\r\n"), 0o600))
	t.Setenv("DATABASE_URL_FILE", path)

	config, err := application.LoadConfig()
	require.Error(t, err)
	assert.Zero(t, config)
	assert.NotContains(t, err.Error(), "private-fixture")
	assert.NotContains(t, err.Error(), path)
}
