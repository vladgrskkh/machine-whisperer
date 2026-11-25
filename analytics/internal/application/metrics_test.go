package application //nolint:testpackage // Wire routes with an isolated registry without connecting to ClickHouse.

import (
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"
	dto "github.com/prometheus/client_model/go"
	"github.com/prometheus/common/expfmt"
	"github.com/prometheus/common/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vladgrskkh/machine-whisperer/analytics/internal/infrastructure/metrics"
)

const (
	batchPath = "/internal/v1/telemetry-batches"
	testToken = "Bearer test-token"
)

func TestMetricsUseIndependentRegistriesAndBoundedHTTPLabels(t *testing.T) {
	t.Parallel()
	registry := metrics.New()
	first := &Application{ingestKey: "test-token", metrics: registry}
	firstMetrics := newMetricsServer(t, registry.Handler())
	secondMetrics := newMetricsServer(t, metrics.New().Handler())
	business := httptest.NewServer(first.Routes())
	t.Cleanup(business.Close)

	for _, tc := range []struct {
		method, path, token, body string
		status                    int
	}{
		{http.MethodGet, "/healthz", "", "", http.StatusOK},
		{http.MethodGet, "/missing/" + uuid.NewString(), "", "", http.StatusNotFound},
		{http.MethodGet, batchPath, testToken, "", http.StatusMethodNotAllowed},
		{"CUSTOM-" + uuid.NewString(), "/healthz", "", "", http.StatusMethodNotAllowed},
		{http.MethodGet, "/metrics", "", "", http.StatusNotFound},
		{http.MethodPost, batchPath, "", "", http.StatusUnauthorized},
		{http.MethodPost, batchPath, testToken, "{}", http.StatusBadRequest},
		{http.MethodPost, batchPath, testToken, strings.Repeat(" ", (16<<20)+1), http.StatusRequestEntityTooLarge},
	} {
		assert.Equal(t, tc.status, requestStatus(t, business, tc.method, tc.path, tc.token, tc.body))
	}
	var group sync.WaitGroup
	for range 8 {
		group.Go(func() {
			path := "/internal/v1/machines/" + uuid.NewString() + "/telemetry?cursor=" + uuid.NewString()
			assert.Equal(t, http.StatusUnauthorized, requestStatus(t, business, http.MethodGet, path, "", ""))
		})
	}
	group.Wait()

	families := scrapeMetrics(t, firstMetrics)
	metricFamily(t, families, "go_goroutines", dto.MetricType_GAUGE)
	metricFamily(t, families, "process_start_time_seconds", dto.MetricType_GAUGE)
	assertHTTPMetrics(t, families)
	assert.Equal(t, map[string]float64{
		"stored": 0, "storage_error": 0,
	}, batchOutcomes(t, families))
	assert.NotContains(t, families, "mw_clickhouse_pool_connections")

	independent := scrapeMetrics(t, secondMetrics)
	assert.NotContains(t, independent, "mw_http_requests_total")
	assert.Equal(t, map[string]float64{
		"stored": 0, "storage_error": 0,
	}, batchOutcomes(t, independent))

	assert.Equal(t, http.StatusNotFound, requestStatus(t, firstMetrics, http.MethodGet, "/healthz", "", ""))
	assert.Equal(t, http.StatusNotFound, requestStatus(t, firstMetrics, http.MethodGet, "/metrics/", "", ""))
	assert.Equal(t, http.StatusMethodNotAllowed, requestStatus(t, firstMetrics, http.MethodPost, "/metrics", "", ""))
	assertHTTPMetrics(t, scrapeMetrics(t, firstMetrics))
}

func assertHTTPMetrics(t *testing.T, families map[string]*dto.MetricFamily) {
	t.Helper()
	requests := metricFamily(t, families, "mw_http_requests_total", dto.MetricType_COUNTER)
	got := make(map[[3]string]float64)
	for _, sample := range requests.GetMetric() {
		labels := metricLabels(sample)
		assert.Len(t, labels, 3)
		got[[3]string{labels["method"], labels["route"], labels["status_class"]}] = sample.GetCounter().GetValue()
	}
	assert.Equal(t, map[[3]string]float64{
		{"GET", "/healthz", "2xx"}:                                     1,
		{"GET", "unmatched", "4xx"}:                                    3,
		{"OTHER", "unmatched", "4xx"}:                                  1,
		{"POST", batchPath, "4xx"}:                                     3,
		{"GET", "/internal/v1/machines/{machine_id}/telemetry", "4xx"}: 8,
	}, got)

	inFlight := metricFamily(t, families, "mw_http_requests_in_flight", dto.MetricType_GAUGE)
	require.Len(t, inFlight.GetMetric(), 1)
	assert.Zero(t, inFlight.GetMetric()[0].GetGauge().GetValue())

	durations := metricFamily(t, families, "mw_http_request_duration_seconds", dto.MetricType_HISTOGRAM)
	require.Len(t, durations.GetMetric(), len(got))
	for _, sample := range durations.GetMetric() {
		labels := metricLabels(sample)
		assert.Len(t, labels, 2)
		histogram := sample.GetHistogram()
		var count float64
		for httpLabels, value := range got {
			if httpLabels[0] == labels["method"] && httpLabels[1] == labels["route"] {
				count += value
			}
		}
		assert.Positive(t, count)
		assert.InDelta(t, count, histogram.GetSampleCount(), 0)
		var buckets []float64
		for _, bucket := range histogram.GetBucket() {
			buckets = append(buckets, bucket.GetUpperBound())
		}
		assert.Equal(t, []float64{0.005, 0.025, 0.1, 0.25, 0.5, 1, 2.5, 5, 10, 25, 30, 60, math.Inf(1)}, buckets)
	}
}

func requestStatus(t *testing.T, srv *httptest.Server, method, path, token, data string) int {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), method, srv.URL+path, strings.NewReader(data))
	if !assert.NoError(t, err) {
		return 0
	}
	if token != "" {
		req.Header.Set("Authorization", token)
	}
	res, err := srv.Client().Do(req)
	if !assert.NoError(t, err) {
		return 0
	}
	_ = res.Body.Close()
	if res.StatusCode == http.StatusRequestEntityTooLarge {
		assert.True(t, res.Close, "oversized requests close the HTTP/1.1 connection")
	}
	return res.StatusCode
}

func newMetricsServer(t *testing.T, handler http.Handler) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return srv
}

func scrapeMetrics(t *testing.T, srv *httptest.Server) map[string]*dto.MetricFamily {
	t.Helper()
	res, err := srv.Client().Get(srv.URL + "/metrics")
	require.NoError(t, err)
	defer res.Body.Close()
	require.Equal(t, http.StatusOK, res.StatusCode)
	return parseMetrics(t, res.Body)
}

func parseMetrics(t *testing.T, body io.Reader) map[string]*dto.MetricFamily {
	t.Helper()
	parser := expfmt.NewTextParser(model.UTF8Validation)
	families, err := parser.TextToMetricFamilies(body)
	require.NoError(t, err)
	return families
}

func metricFamily(
	t *testing.T, families map[string]*dto.MetricFamily, name string, kind dto.MetricType,
) *dto.MetricFamily {
	t.Helper()
	family := families[name]
	require.NotNil(t, family, name)
	assert.Equal(t, kind, family.GetType(), name)
	return family
}

func batchOutcomes(t *testing.T, families map[string]*dto.MetricFamily) map[string]float64 {
	t.Helper()
	values := make(map[string]float64)
	for _, sample := range metricFamily(t, families, "mw_telemetry_batches_total", dto.MetricType_COUNTER).GetMetric() {
		labels := metricLabels(sample)
		assert.Len(t, labels, 1)
		assert.Contains(t, labels, "outcome")
		values[labels["outcome"]] = sample.GetCounter().GetValue()
	}
	return values
}

func metricLabels(sample *dto.Metric) map[string]string {
	labels := make(map[string]string, len(sample.GetLabel()))
	for _, label := range sample.GetLabel() {
		labels[label.GetName()] = label.GetValue()
	}
	return labels
}
