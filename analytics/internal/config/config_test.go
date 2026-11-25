package config_test

import (
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vladgrskkh/machine-whisperer/analytics/internal/config"
)

func TestServiceConfiguration(t *testing.T) {
	t.Setenv("CLICKHOUSE_PASSWORD_FILE", "")
	t.Setenv("ANALYTICS_INGEST_API_KEY_FILE", "")
	t.Setenv("CLICKHOUSE_ADDRESS", "localhost:9000")
	t.Setenv("CLICKHOUSE_DATABASE", "history")
	t.Setenv("CLICKHOUSE_USER", "ingest")
	t.Setenv("CLICKHOUSE_PASSWORD", "secret")
	t.Setenv("ANALYTICS_ADDRESS", "")
	t.Setenv("ANALYTICS_METRICS_ADDRESS", "")
	t.Setenv("ANALYTICS_INGEST_API_KEY", "relay")
	cfg, err := config.Load()
	require.NoError(t, err)
	assert.Equal(t, ":8081", cfg.Address)
	assert.Equal(t, "127.0.0.1:9091", cfg.MetricsAddress)
	t.Setenv("ANALYTICS_METRICS_ADDRESS", "analytics-metrics:9091")
	cfg, err = config.Load()
	require.NoError(t, err)
	assert.Equal(t, "analytics-metrics:9091", cfg.MetricsAddress)
	for _, name := range []string{"CLICKHOUSE_ADDRESS", "CLICKHOUSE_DATABASE", "CLICKHOUSE_USER", "CLICKHOUSE_PASSWORD", "ANALYTICS_INGEST_API_KEY"} {
		t.Run(name, func(t *testing.T) {
			t.Setenv(name, "")
			_, loadErr := config.Load()
			assert.Error(t, loadErr)
		})
	}
	t.Setenv("ANALYTICS_INGEST_API_KEY", " bad token ")
	_, err = config.Load()
	assert.Error(t, err)
	_, err = config.LoadClickHouse()
	assert.NoError(t, err)
}

const generatedPassword = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

func TestLoadClickHousePreservesEnvironmentPassword(t *testing.T) {
	t.Setenv("CLICKHOUSE_ADDRESS", "localhost:9000")
	t.Setenv("CLICKHOUSE_DATABASE", "history")
	t.Setenv("CLICKHOUSE_USER", "ingest")
	t.Setenv("CLICKHOUSE_PASSWORD_FILE", "")
	const password = "env\rpassword"
	t.Setenv("CLICKHOUSE_PASSWORD", password)

	cfg, err := config.LoadClickHouse()
	require.NoError(t, err)
	assert.Equal(t, password, cfg.Password)
}

func TestLoadClickHouseReadsPasswordFile(t *testing.T) {
	t.Setenv("CLICKHOUSE_ADDRESS", "localhost:9000")
	t.Setenv("CLICKHOUSE_DATABASE", "history")
	t.Setenv("CLICKHOUSE_USER", "ingest")
	t.Setenv("CLICKHOUSE_PASSWORD", "env-password")
	t.Setenv("ANALYTICS_INGEST_API_KEY", "")
	t.Setenv("ANALYTICS_INGEST_API_KEY_FILE", filepath.Join(t.TempDir(), "missing"))

	for _, tt := range []struct {
		name   string
		suffix string
	}{
		{name: "no line ending"},
		{name: "LF", suffix: "\n"},
		{name: "CRLF", suffix: "\r\n"},
		{name: "trailing line endings", suffix: "\r\n\n\r"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "password")
			require.NoError(t, os.WriteFile(path, []byte(generatedPassword+tt.suffix), 0o600))
			t.Setenv("CLICKHOUSE_PASSWORD_FILE", path)

			cfg, err := config.LoadClickHouse()
			require.NoError(t, err)
			assert.Equal(t, generatedPassword, cfg.Password)
		})
	}
}

func TestLoadReadsIngestKeyFile(t *testing.T) {
	t.Setenv("CLICKHOUSE_ADDRESS", "localhost:9000")
	t.Setenv("CLICKHOUSE_DATABASE", "history")
	t.Setenv("CLICKHOUSE_USER", "ingest")
	t.Setenv("CLICKHOUSE_PASSWORD", "secret")
	t.Setenv("CLICKHOUSE_PASSWORD_FILE", "")
	t.Setenv("ANALYTICS_INGEST_API_KEY", "env-token")
	path := filepath.Join(t.TempDir(), "token")
	require.NoError(t, os.WriteFile(path, []byte("file-token\r\n"), 0o600))
	t.Setenv("ANALYTICS_INGEST_API_KEY_FILE", path)

	cfg, err := config.Load()
	require.NoError(t, err)
	assert.Equal(t, "file-token", cfg.IngestKey)
}

func TestLoadRejectsInvalidSecretFiles(t *testing.T) {
	t.Setenv("CLICKHOUSE_ADDRESS", "localhost:9000")
	t.Setenv("CLICKHOUSE_DATABASE", "history")
	t.Setenv("CLICKHOUSE_USER", "ingest")
	t.Setenv("CLICKHOUSE_PASSWORD", "secret")
	t.Setenv("CLICKHOUSE_PASSWORD_FILE", "")
	t.Setenv("ANALYTICS_INGEST_API_KEY", "env-token")
	t.Setenv("ANALYTICS_INGEST_API_KEY_FILE", "")

	for _, tt := range []struct {
		name string
		env  string
		data string
	}{
		{name: "missing password file", env: "CLICKHOUSE_PASSWORD_FILE"},
		{name: "missing token file", env: "ANALYTICS_INGEST_API_KEY_FILE"},
		{name: "empty password file", env: "CLICKHOUSE_PASSWORD_FILE", data: "\r\n"},
		{name: "short password file", env: "CLICKHOUSE_PASSWORD_FILE", data: generatedPassword[:63]},
		{name: "long password file", env: "CLICKHOUSE_PASSWORD_FILE", data: generatedPassword + "0\r\n"},
		{name: "uppercase password file", env: "CLICKHOUSE_PASSWORD_FILE", data: generatedPassword[:63] + "F"},
		{name: "non-hex password file", env: "CLICKHOUSE_PASSWORD_FILE", data: generatedPassword[:63] + "g"},
		{name: "XML-sensitive password file", env: "CLICKHOUSE_PASSWORD_FILE", data: generatedPassword[:63] + "&"},
		{name: "whitespace in password file", env: "CLICKHOUSE_PASSWORD_FILE", data: generatedPassword[:63] + " \r\n"},
		{name: "password with embedded LF", env: "CLICKHOUSE_PASSWORD_FILE", data: generatedPassword[:62] + "\nf\r\n"},
		{name: "password with NUL byte", env: "CLICKHOUSE_PASSWORD_FILE", data: "fixture-token\x00suffix\r\n"},
		{name: "password with embedded CR", env: "CLICKHOUSE_PASSWORD_FILE", data: generatedPassword[:62] + "\rf\r\n"},
		{name: "password with embedded CRLF", env: "CLICKHOUSE_PASSWORD_FILE", data: "fixture-token\r\nsuffix\r\n"},
		{name: "token with NUL byte", env: "ANALYTICS_INGEST_API_KEY_FILE", data: "fixture-token\x00suffix\r\n"},
		{name: "token with trailing space", env: "ANALYTICS_INGEST_API_KEY_FILE", data: "fixture-token \n"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "secret")
			if tt.data != "" {
				require.NoError(t, os.WriteFile(path, []byte(tt.data), 0o600))
			}
			t.Setenv(tt.env, path)

			_, err := config.Load()
			require.Error(t, err)
			assert.NotErrorIs(t, err, fs.ErrNotExist)
			assert.NotContains(t, err.Error(), path)
			assert.NotContains(t, err.Error(), "fixture-token")
			assert.NotContains(t, err.Error(), generatedPassword[:16])
		})
	}
}
