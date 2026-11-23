package config_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vladgrskkh/machine-whisperer/analytics/internal/config"
)

func TestServiceConfiguration(t *testing.T) {
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
