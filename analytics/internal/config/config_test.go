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
	t.Setenv("ANALYTICS_INGEST_API_KEY", "relay")
	cfg, err := config.Load()
	require.NoError(t, err)
	assert.Equal(t, ":8081", cfg.Address)
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

func TestLoadClickHouseReadsPasswordFile(t *testing.T) {
	t.Setenv("CLICKHOUSE_ADDRESS", "localhost:9000")
	t.Setenv("CLICKHOUSE_DATABASE", "history")
	t.Setenv("CLICKHOUSE_USER", "ingest")
	t.Setenv("CLICKHOUSE_PASSWORD", "")
	t.Setenv("ANALYTICS_INGEST_API_KEY", "")
	t.Setenv("ANALYTICS_INGEST_API_KEY_FILE", filepath.Join(t.TempDir(), "missing"))
	path := filepath.Join(t.TempDir(), "password")
	require.NoError(t, os.WriteFile(path, []byte(" secret\t \r\n\r\n"), 0o600))
	t.Setenv("CLICKHOUSE_PASSWORD_FILE", path)

	cfg, err := config.LoadClickHouse()
	require.NoError(t, err)
	assert.Equal(t, " secret\t ", cfg.Password)
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
		})
	}
}
