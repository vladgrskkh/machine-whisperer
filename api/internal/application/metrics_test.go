package application_test

import (
	"math"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	dto "github.com/prometheus/client_model/go"
	"github.com/prometheus/common/expfmt"
	"github.com/prometheus/common/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vladgrskkh/machine-whisperer/api/internal/application"
	"github.com/vladgrskkh/machine-whisperer/api/internal/testutil"
)

func TestApplicationMetricsUseIndependentRegistriesAndBoundedRoutes(t *testing.T) {
	t.Parallel()
	id, signing, verifying, _ := testutil.AuthKeys(t)
	poolConfig, err := pgxpool.ParseConfig("postgres://unused:unused@localhost/unused?sslmode=disable")
	require.NoError(t, err)
	poolConfig.ConnConfig.Host = t.TempDir()
	pool, err := pgxpool.NewWithConfig(t.Context(), poolConfig)
	require.NoError(t, err)
	t.Cleanup(pool.Close)
	config := application.Config{
		Auth: application.AuthConfig{SigningKeyID: id, SigningKeyFile: signing, VerifyKeysFile: verifying},
	}
	first, err := application.New(t.Context(), config, application.WithPool(pool))
	require.NoError(t, err)
	t.Cleanup(first.Close)
	second, err := application.New(t.Context(), config, application.WithPool(pool))
	require.NoError(t, err)
	t.Cleanup(second.Close)
	firstMetrics := newMetricsServer(t, first)
	secondMetrics := newMetricsServer(t, second)
	server := httptest.NewServer(first.Routes())
	t.Cleanup(server.Close)

	cases := []struct {
		method, path string
		status       int
	}{
		{http.MethodGet, "/healthz", http.StatusOK},
		{http.MethodGet, "/missing/" + uuid.NewString(), http.StatusNotFound},
		{http.MethodGet, "/v1/auth/login/missing", http.StatusNotFound},
		{http.MethodPut, "/v1/auth/login", http.StatusMethodNotAllowed},
		{http.MethodPost, "/v1/auth/login", http.StatusBadRequest},
		{http.MethodDelete, "/healthz", http.StatusMethodNotAllowed},
		{http.MethodDelete, "/v1/machines/" + uuid.NewString(), http.StatusUnauthorized},
		{"custom-" + uuid.NewString(), "/healthz", http.StatusMethodNotAllowed},
		{http.MethodGet, "/metrics", http.StatusNotFound},
	}
	for _, tc := range cases {
		request, requestErr := http.NewRequestWithContext(t.Context(), tc.method, server.URL+tc.path, nil)
		require.NoError(t, requestErr)
		response, requestErr := server.Client().Do(request)
		require.NoError(t, requestErr)
		_ = response.Body.Close()
		assert.Equal(t, tc.status, response.StatusCode)
	}
	var requests sync.WaitGroup
	for range 8 {
		requests.Go(func() {
			path := "/v1/machines/" + uuid.NewString() + "/telemetry"
			response, postErr := server.Client().Post(server.URL+path+"?machine=hidden", "application/json", nil)
			if assert.NoError(t, postErr) {
				_ = response.Body.Close()
				assert.Equal(t, http.StatusUnauthorized, response.StatusCode)
			}
		})
	}
	requests.Wait()

	families := scrapeMetrics(t, firstMetrics)
	assertMetricFamily(t, families, "go_goroutines", dto.MetricType_GAUGE)
	assertMetricFamily(t, families, "process_start_time_seconds", dto.MetricType_GAUGE)
	assertRequestDurations(t, families)
	requestsFamily := assertMetricFamily(t, families, "mw_http_requests_total", dto.MetricType_COUNTER)
	got := make(map[[3]string]float64)
	for _, sample := range requestsFamily.GetMetric() {
		labels := metricLabels(sample)
		assert.Len(t, labels, 3)
		got[[3]string{labels["method"], labels["route"], labels["status_class"]}] = sample.GetCounter().GetValue()
	}
	assert.Equal(t, map[[3]string]float64{
		{"DELETE", "/v1/machines/{id}", "4xx"}:                 1,
		{"DELETE", "unmatched", "4xx"}:                         1,
		{"GET", "/healthz", "2xx"}:                             1,
		{"GET", "unmatched", "4xx"}:                            3,
		{"PUT", "unmatched", "4xx"}:                            1,
		{"OTHER", "unmatched", "4xx"}:                          1,
		{"POST", "/v1/auth/login", "4xx"}:                      1,
		{"POST", "/v1/machines/{machine_id}/telemetry", "4xx"}: 8,
	}, got)
	inFlight := assertMetricFamily(t, families, "mw_http_requests_in_flight", dto.MetricType_GAUGE)
	require.Len(t, inFlight.GetMetric(), 1)
	assert.Zero(t, inFlight.GetMetric()[0].GetGauge().GetValue())
	ingest := assertMetricFamily(t, families, "mw_telemetry_ingest_requests_total", dto.MetricType_COUNTER)
	ingestOutcomes := make(map[string]float64)
	for _, sample := range ingest.GetMetric() {
		labels := metricLabels(sample)
		assert.Len(t, labels, 1)
		ingestOutcomes[labels["outcome"]] = sample.GetCounter().GetValue()
	}
	assert.Equal(t, map[string]float64{
		"accepted_new": 0, "accepted_replay": 0, "invalid": 0,
		"machine_not_found": 0, "conflict": 0, "outside_window": 0, "out_of_order": 0, "internal_error": 0,
	}, ingestOutcomes)
	assert.NotContains(t, families, "mw_postgres_pool_connections")
	timestamp := assertMetricFamily(t, families, "mw_outbox_snapshot_timestamp_seconds", dto.MetricType_GAUGE)
	require.Len(t, timestamp.GetMetric(), 1)
	assert.Zero(t, timestamp.GetMetric()[0].GetGauge().GetValue())
	assert.NotContains(t, families, "mw_outbox_pending_events")
	failures := assertMetricFamily(t, families, "mw_outbox_snapshot_errors_total", dto.MetricType_COUNTER)
	require.Len(t, failures.GetMetric(), 1)
	assert.InDelta(t, 1, failures.GetMetric()[0].GetCounter().GetValue(), 0)
	assert.NotContains(t, scrapeMetrics(t, secondMetrics), "mw_http_requests_total")
	for _, path := range []string{"/healthz", "/metrics/"} {
		response, getErr := firstMetrics.Client().Get(firstMetrics.URL + path)
		require.NoError(t, getErr)
		_ = response.Body.Close()
		assert.Equal(t, http.StatusNotFound, response.StatusCode)
	}
	assert.Len(t, scrapeMetrics(t, firstMetrics)["mw_http_requests_total"].GetMetric(), 8)
}

func newMetricsServer(t *testing.T, app *application.Application) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(app.MetricsHandler())
	t.Cleanup(server.Close)
	return server
}

func scrapeMetrics(t *testing.T, server *httptest.Server) map[string]*dto.MetricFamily {
	t.Helper()
	response, err := server.Client().Get(server.URL + "/metrics")
	require.NoError(t, err)
	defer response.Body.Close()
	require.Equal(t, http.StatusOK, response.StatusCode)
	parser := expfmt.NewTextParser(model.UTF8Validation)
	families, err := parser.TextToMetricFamilies(response.Body)
	require.NoError(t, err)
	return families
}

func assertMetricFamily(
	t *testing.T, families map[string]*dto.MetricFamily, name string, kind dto.MetricType,
) *dto.MetricFamily {
	t.Helper()
	family := families[name]
	require.NotNil(t, family, name)
	assert.Equal(t, kind, family.GetType(), name)
	return family
}

func assertRequestDurations(t *testing.T, families map[string]*dto.MetricFamily) {
	t.Helper()
	durations := assertMetricFamily(t, families, "mw_http_request_duration_seconds", dto.MetricType_HISTOGRAM)
	require.Len(t, durations.GetMetric(), 8)
	for _, sample := range durations.GetMetric() {
		assert.Len(t, sample.GetLabel(), 2)
		histogram := sample.GetHistogram()
		if metricLabels(sample)["route"] == "/healthz" {
			assert.EqualValues(t, 1, histogram.GetSampleCount())
		}
		bounds := make([]float64, 0, len(histogram.GetBucket()))
		for _, bucket := range histogram.GetBucket() {
			bounds = append(bounds, bucket.GetUpperBound())
		}
		assert.Equal(t, []float64{0.005, 0.025, 0.1, 0.25, 0.5, 1, 2.5, 5, 10, 25, 30, 60, math.Inf(1)}, bounds)
	}
}

func metricLabels(sample *dto.Metric) map[string]string {
	labels := make(map[string]string, len(sample.GetLabel()))
	for _, label := range sample.GetLabel() {
		labels[label.GetName()] = label.GetValue()
	}
	return labels
}
