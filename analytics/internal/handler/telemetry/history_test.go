package telemetry_test

import (
	"encoding/base64"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vladgrskkh/machine-whisperer/analytics/internal/application"
	"github.com/vladgrskkh/machine-whisperer/analytics/internal/handler/telemetry"
)

const (
	historyMachine = "fecbfd44-b504-472d-ac63-eebdbb96dfc7"
	historyRoute   = "/internal/v1/machines/{machine_id}/telemetry"
	historyURL     = "/internal/v1/machines/" + historyMachine + "/telemetry"
	cursorJSON     = `{"machine_id":"` + historyMachine + `","observed_at":"2026-11-03T03:31:23.123456Z",` +
		`"device_id":"ce005098-0257-4cbe-9d08-414068d84722","event_id":"c89ad15e-4cff-4364-b959-ac185e98c511"}`
)

func TestAnalyticsHistoryRequiresCredential(t *testing.T) {
	app := &application.Application{}
	srv := httptest.NewServer(app.Routes())
	defer srv.Close()
	assertHistoryStatus(t, srv, historyURL, http.StatusUnauthorized)
}

func TestAnalyticsHistoryRejectsInvalidCursors(t *testing.T) {
	router := chi.NewRouter()
	router.Get(historyRoute, telemetry.NewHistory(nil).ServeHTTP)
	srv := httptest.NewServer(router)
	defer srv.Close()
	for name, token := range map[string]string{
		"malformed base64":   "not%base64",
		"newline in token":   cursorToken(cursorJSON) + "\n",
		"oversized token":    strings.Repeat("A", 513),
		"invalid JSON":       cursorToken("{"),
		"extra JSON":         cursorToken(cursorJSON + "{}"),
		"unknown field":      cursorToken(strings.Replace(cursorJSON, "event_id", "unknown", 1)),
		"duplicate field":    cursorToken(strings.Replace(cursorJSON, "{", `{"machine_id":"`+historyMachine+`",`, 1)),
		"wrong machine":      cursorToken(strings.Replace(cursorJSON, historyMachine, "05584bee-bfb0-4521-9281-ad359b2236cb", 1)),
		"invalid device":     cursorToken(strings.Replace(cursorJSON, "ce005098", "bad", 1)),
		"noncanonical event": cursorToken(strings.Replace(cursorJSON, "c89ad15e", "C89AD15E", 1)),
		"invalid timestamp":  cursorToken(strings.Replace(cursorJSON, "T03:31:23", "T3:31:23", 1)),
		"out of range":       cursorToken(strings.Replace(cursorJSON, "2026-11-03", "1800-11-03", 1)),
		"missing fields":     cursorToken(`{"machine_id":"` + historyMachine + `"}`),
		"empty token":        "",
	} {
		t.Run(name, func(t *testing.T) {
			assertHistoryStatus(t, srv, historyURL+"?cursor="+url.QueryEscape(token), http.StatusBadRequest)
		})
	}
	for name, path := range map[string]string{
		"invalid machine":    strings.Replace(historyURL, historyMachine, "bad-machine", 1),
		"negative limit":     historyURL + "?limit=-1",
		"zero limit":         historyURL + "?limit=0",
		"over maximum":       historyURL + "?limit=1001",
		"nonnumeric limit":   historyURL + "?limit=one",
		"empty limit":        historyURL + "?limit=",
		"repeated limit":     historyURL + "?limit=1&limit=10",
		"repeated cursor":    historyURL + "?cursor=" + cursorToken(cursorJSON) + "&cursor=" + cursorToken(cursorJSON),
		"bad percent escape": historyURL + "?cursor=%XY",
	} {
		t.Run(name, func(t *testing.T) {
			assertHistoryStatus(t, srv, path, http.StatusBadRequest)
		})
	}
}

func cursorToken(value string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(value))
}

func assertHistoryStatus(t *testing.T, srv *httptest.Server, path string, want int) {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, srv.URL+path, nil)
	require.NoError(t, err)
	resp, err := srv.Client().Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	assert.Equal(t, want, resp.StatusCode, string(body))
}
