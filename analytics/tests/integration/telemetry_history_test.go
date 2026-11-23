//go:build integration

package integration_test

import (
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/ClickHouse/clickhouse-go/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	tc "github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/vladgrskkh/machine-whisperer/analytics/internal/application"
	"github.com/vladgrskkh/machine-whisperer/analytics/internal/config"
	"github.com/vladgrskkh/machine-whisperer/analytics/migrations"
)

const (
	otherMachine = "cc828e2e-dffc-4abc-b456-52a3551bce10"
	historyPath  = "/internal/v1/machines/" + machine + "/telemetry"
)

type historyPage struct {
	Items      []map[string]json.RawMessage `json:"items"`
	NextCursor *string                      `json:"next_cursor"`
}

func TestAnalyticsHistoryQueriesPages(t *testing.T) {
	srv, conn := historyServer(t)
	status, reply := getHistory(t, srv, historyPath, bearer)
	require.Equal(t, http.StatusServiceUnavailable, status)
	assert.NotContains(t, reply, "telemetry_events")
	assert.NotContains(t, reply, "fixture-password")

	require.NoError(t, conn.Exec(t.Context(), migrations.CreateTelemetryEvents))
	require.NoError(t, conn.Exec(t.Context(), "SYSTEM STOP MERGES telemetry_events"))
	require.NoError(t, conn.Exec(t.Context(),
		"ALTER TABLE telemetry_events MODIFY SETTING non_replicated_deduplication_window = 0"))
	t.Run("credential", func(t *testing.T) {
		for _, headers := range [][]string{nil, {"Bearer wrong"}, {"Basic " + ingestKey}, {bearer, bearer}} {
			status, _ := getHistory(t, srv, historyPath, headers...)
			assert.Equal(t, http.StatusUnauthorized, status)
		}
	})
	t.Run("pages", func(t *testing.T) {
		assertHistoryPages(t, srv, conn)
	})
	t.Run("page limits", func(t *testing.T) {
		assertHistoryLimits(t, srv, conn)
	})
	t.Run("empty history", func(t *testing.T) {
		const emptyMachine = "49b48e81-fd84-4784-a661-4d4f7591abde"
		status, reply := getHistory(t, srv, "/internal/v1/machines/"+emptyMachine+"/telemetry?limit=1000", bearer)
		require.Equal(t, http.StatusOK, status, reply)
		assert.JSONEq(t, `{"items":[],"next_cursor":null}`, reply)
	})
}

func assertHistoryPages(t *testing.T, srv *httptest.Server, conn clickhouse.Conn) {
	t.Helper()
	// Native UUID order compares the second half first. Lexical order
	// would reverse both the device boundary and the same-device event boundary.
	const (
		deviceHigh = "00000000-0000-4000-8000-000000000002"
		deviceLow  = "ffffffff-ffff-4fff-8000-000000000001"
		eventHigh  = "00000000-0000-4000-8000-000000000004"
		eventLow   = "ffffffff-ffff-4fff-8000-000000000003"
	)
	replaceTie := strings.NewReplacer(device, deviceHigh, first, eventHigh, "1.25", "null")
	high := replaceTie.Replace(firstEvent)
	low := strings.ReplaceAll(high, eventHigh, eventLow)
	other := strings.ReplaceAll(high, deviceHigh, deviceLow)
	older := strings.ReplaceAll(firstEvent, ".123456", ".123455")
	unrelated := strings.ReplaceAll(secondEvent, machine, otherMachine)
	body := `{"events":[` + strings.Join([]string{older, other, low, high, secondEvent, unrelated}, ",") + `]}`
	for range 2 {
		status, reply := post(t, srv, body, bearer)
		require.Equal(t, http.StatusNoContent, status, reply)
	}

	var physicalRows uint64
	require.NoError(t, conn.QueryRow(t.Context(), "SELECT count() FROM telemetry_events").Scan(&physicalRows))
	assert.Equal(t, uint64(12), physicalRows)

	// Each limit-one request crosses a distinct component of the tuple.
	next := historyPath + "?limit=1"
	for i, want := range []struct {
		deviceID string
		eventID  string
		observed string
	}{
		{device, second, "2026-11-03T03:32:00Z"},
		{deviceHigh, eventHigh, "2026-11-03T03:31:23.123456Z"},
		{deviceHigh, eventLow, "2026-11-03T03:31:23.123456Z"},
		{deviceLow, eventHigh, "2026-11-03T03:31:23.123456Z"},
		{device, first, "2026-11-03T03:31:23.123455Z"},
	} {
		status, reply := getHistory(t, srv, next, bearer)
		require.Equal(t, http.StatusOK, status, reply)
		var page historyPage
		require.NoError(t, json.Unmarshal([]byte(reply), &page))
		require.Len(t, page.Items, 1)
		item := page.Items[0]
		assert.Equal(t, `"`+want.deviceID+`"`, string(item["device_id"]))
		assert.Equal(t, `"`+want.eventID+`"`, string(item["event_id"]))
		assert.Equal(t, `"`+want.observed+`"`, string(item["observed_at"]))
		if i == 1 {
			assert.Len(t, item, 9)
			assert.Equal(t, `"2026-11-03T03:31:24.765432Z"`, string(item["received_at"]))
			assert.Equal(t, "1", string(item["schema_version"]))
			assert.Equal(t, `"running"`, string(item["operating_state"]))
			assert.Equal(t, "0.75", string(item["vibration_rms_g"]))
			assert.Equal(t, "null", string(item["dominant_frequency_hz"]))
			assert.Equal(t, "null", string(item["current_rms_a"]))
		}
		if i == 4 {
			assert.Nil(t, page.NextCursor)
			return
		}
		require.NotNil(t, page.NextCursor)
		token := *page.NextCursor
		if i == 1 {
			raw, err := base64.RawURLEncoding.DecodeString(token)
			require.NoError(t, err)
			const observed = "2026-11-03T03:31:23.123456Z"
			require.Contains(t, string(raw), observed)
			modified := strings.Replace(string(raw), observed, "2026-11-03T06:31:23.123456999+03:00", 1)
			token = base64.RawURLEncoding.EncodeToString([]byte(modified))
		}
		next = historyPath + "?limit=1&cursor=" + url.QueryEscape(token)
	}
}

func assertHistoryLimits(t *testing.T, srv *httptest.Server, conn clickhouse.Conn) {
	t.Helper()
	const limitMachine = "aaf1ed92-7af9-44e4-b36c-1804f785aa69"
	require.NoError(t, conn.Exec(t.Context(), `
		INSERT INTO telemetry_events
			(schema_version, event_id, device_id, machine_id, observed_at, received_at, operating_state,
			 vibration_rms_g, dominant_frequency_hz, current_rms_a)
		SELECT 1, toUUID('741f13de-f929-4b89-974d-170a93e147ab'), toUUID('89ce1f51-d524-4ea5-93d1-281d5f6a3873'),
		       toUUID('`+limitMachine+`'), toDateTime64('2026-11-03 03:31:23', 6, 'UTC') + toIntervalSecond(number),
		       toDateTime64('2026-11-03 03:31:23', 6, 'UTC'), 'running', 0, NULL, NULL
		FROM numbers(1001)`))
	path := "/internal/v1/machines/" + limitMachine + "/telemetry"
	for _, tc := range []struct {
		query string
		items int
	}{
		{"", 100},
		{"?limit=1000", 1000},
	} {
		status, reply := getHistory(t, srv, path+tc.query, bearer)
		require.Equal(t, http.StatusOK, status, reply)
		var page historyPage
		require.NoError(t, json.Unmarshal([]byte(reply), &page))
		assert.Len(t, page.Items, tc.items)
		assert.NotNil(t, page.NextCursor)
	}
}

func historyServer(t *testing.T) (*httptest.Server, clickhouse.Conn) {
	t.Helper()
	ctx := t.Context()
	env := map[string]string{
		"CLICKHOUSE_DB":       "history",
		"CLICKHOUSE_USER":     "analytics",
		"CLICKHOUSE_PASSWORD": "fixture-password",
	}
	container, err := tc.Run(ctx, image, tc.WithEnv(env), tc.WithExposedPorts("9000/tcp", "8123/tcp"),
		tc.WithWaitStrategy(wait.ForHTTP("/ping").WithPort("8123/tcp").WithStartupTimeout(2*time.Minute)))
	tc.CleanupContainer(t, container)
	require.NoError(t, err)
	address, err := container.PortEndpoint(ctx, "9000/tcp", "")
	require.NoError(t, err)
	cfg := config.ClickHouse{
		Address: address, Database: env["CLICKHOUSE_DB"],
		User: env["CLICKHOUSE_USER"], Password: env["CLICKHOUSE_PASSWORD"],
	}
	const dialTimeout = 10 * time.Second
	conn, err := clickhouse.Open(&clickhouse.Options{
		Addr:        []string{cfg.Address},
		Auth:        clickhouse.Auth{Database: cfg.Database, Username: cfg.User, Password: cfg.Password},
		DialTimeout: dialTimeout,
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })
	app, err := application.New(ctx, config.Config{IngestKey: ingestKey}, application.WithClickHouse(cfg))
	require.NoError(t, err)
	t.Cleanup(app.Close)
	srv := httptest.NewServer(app.Routes())
	t.Cleanup(srv.Close)
	return srv, conn
}

func getHistory(t *testing.T, srv *httptest.Server, path string, headers ...string) (int, string) {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, srv.URL+path, nil)
	require.NoError(t, err)
	for _, header := range headers {
		req.Header.Add("Authorization", header)
	}
	resp, err := srv.Client().Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	return resp.StatusCode, string(data)
}
