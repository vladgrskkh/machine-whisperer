//go:build integration

package integration_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	dto "github.com/prometheus/client_model/go"
	"github.com/prometheus/common/expfmt"
	"github.com/prometheus/common/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func scrapeMetrics(t *testing.T, srv *httptest.Server) map[string]*dto.MetricFamily {
	t.Helper()
	res, err := srv.Client().Get(srv.URL + "/metrics")
	require.NoError(t, err)
	defer res.Body.Close()
	require.Equal(t, http.StatusOK, res.StatusCode)
	parser := expfmt.NewTextParser(model.UTF8Validation)
	families, err := parser.TextToMetricFamilies(res.Body)
	require.NoError(t, err)
	return families
}

func assertClickHouseMetrics(t *testing.T, srv *httptest.Server) {
	t.Helper()
	families := scrapeMetrics(t, srv)
	batches := make(map[string]float64)
	for _, sample := range requireFamily(t, families, "mw_telemetry_batches_total", dto.MetricType_COUNTER) {
		batches[metricLabel(t, sample, "outcome")] = sample.GetCounter().GetValue()
	}
	assert.Equal(t, map[string]float64{
		"stored": 2, "storage_error": 2,
	}, batches)
	stored := requireFamily(t, families, "mw_telemetry_batch_events_stored_total", dto.MetricType_COUNTER)
	require.Len(t, stored, 1)
	assert.Empty(t, stored[0].GetLabel())
	assert.InDelta(t, 4, stored[0].GetCounter().GetValue(), 0)

	assertClickHouseOperationMetrics(t, families)
}

func assertClickHouseOperationMetrics(t *testing.T, families map[string]*dto.MetricFamily) {
	t.Helper()
	operations := make(map[[2]string]float64)
	for _, sample := range requireFamily(t, families, "mw_clickhouse_operations_total", dto.MetricType_COUNTER) {
		assert.Len(t, sample.GetLabel(), 2)
		operation := metricLabel(t, sample, "operation")
		outcome := metricLabel(t, sample, "outcome")
		operations[[2]string{operation, outcome}] = sample.GetCounter().GetValue()
	}
	assert.Equal(t, map[[2]string]float64{
		{"insert_batch", "success"}:  2,
		{"insert_batch", "error"}:    2,
		{"insert_batch", "timeout"}:  0,
		{"insert_batch", "canceled"}: 0,
		{"list_history", "success"}:  1,
		{"list_history", "error"}:    1,
		{"list_history", "timeout"}:  0,
		{"list_history", "canceled"}: 0,
	}, operations)

	durations := make(map[string]uint64)
	for _, sample := range requireFamily(t, families, "mw_clickhouse_operation_duration_seconds", dto.MetricType_HISTOGRAM) {
		assert.Len(t, sample.GetLabel(), 1)
		assert.Positive(t, sample.GetHistogram().GetSampleSum())
		durations[metricLabel(t, sample, "operation")] = sample.GetHistogram().GetSampleCount()
	}
	assert.Equal(t, map[string]uint64{"insert_batch": 4, "list_history": 2}, durations)
}

func requireFamily(
	t *testing.T, families map[string]*dto.MetricFamily, name string, kind dto.MetricType,
) []*dto.Metric {
	t.Helper()
	family := families[name]
	require.NotNil(t, family, name)
	assert.Equal(t, kind, family.GetType(), name)
	return family.GetMetric()
}

func metricLabel(t *testing.T, sample *dto.Metric, key string) string {
	t.Helper()
	for _, label := range sample.GetLabel() {
		if label.GetName() == key {
			return label.GetValue()
		}
	}
	t.Errorf("series is missing the %s label", key)
	return ""
}
