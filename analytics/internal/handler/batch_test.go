package handler_test

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/vladgrskkh/machine-whisperer/analytics/internal/handler"
)

const (
	key    = "relay-key"
	bearer = "Bearer " + key
	event  = `{"device_id":"ce005098-0257-4cbe-9d08-414068d84722","event_id":"c89ad15e-4cff-4364-b959-ac185e98c511","machine_id":"fecbfd44-b504-472d-ac63-eebdbb96dfc7","received_at":"2026-10-01T05:04:03.123456Z","payload":{"schema_version":1,"event_id":"c89ad15e-4cff-4364-b959-ac185e98c511","observed_at":"2026-10-01T08:03:00.112233+03:00","operating_state":"running","vibration_rms_g":0,"dominant_frequency_hz":null,"current_rms_a":2.5}}`
)

func batch(events ...string) string {
	return `{"events":[` + strings.Join(events, ",") + `]}`
}

func post(t *testing.T, srv *httptest.Server, body string, headers ...string) (int, string) {
	t.Helper()
	req, err := http.NewRequestWithContext(
		t.Context(),
		http.MethodPost,
		srv.URL+"/internal/v1/telemetry-batches",
		strings.NewReader(body),
	)
	require.NoError(t, err)
	for _, value := range headers {
		req.Header.Add("Authorization", value)
	}
	resp, err := srv.Client().Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	return resp.StatusCode, string(data)
}

func TestBatchRequiresOneServiceCredential(t *testing.T) {
	srv := httptest.NewServer(handler.New(key, NewMockBatchWriter(t)))
	defer srv.Close()
	for _, headers := range [][]string{nil, {"Bearer wrong"}, {"Basic " + key}, {bearer, bearer}} {
		status, _ := post(t, srv, batch(event), headers...)
		assert.Equal(t, http.StatusUnauthorized, status)
	}
}

func TestBatchAcceptsLimitsAndSanitizesInsertFailures(t *testing.T) {
	writer := NewMockBatchWriter(t)
	writer.On("WriteBatch", mock.Anything, mock.Anything).Return(nil).Once()
	writer.On("WriteBatch", mock.Anything, mock.Anything).Return(errors.New("secret-host measurement")).Once()
	srv := httptest.NewServer(handler.New(key, writer))
	defer srv.Close()
	body := `{"events":[` + strings.Repeat(event+",", 999) + event + `]}`
	body += strings.Repeat(" ", (16<<20)-len(body))
	status, reply := post(t, srv, body, bearer)
	assert.Equal(t, http.StatusNoContent, status)
	assert.Empty(t, reply)
	status, reply = post(t, srv, batch(event), bearer)
	assert.Equal(t, http.StatusServiceUnavailable, status)
	assert.NotContains(t, reply, "secret-host")
	assert.NotContains(t, reply, "measurement")
}

func TestBatchRejectsInvalidRequestsBeforeWriting(t *testing.T) {
	srv := httptest.NewServer(handler.New(key, NewMockBatchWriter(t)))
	defer srv.Close()
	for name, body := range map[string]string{
		"empty": batch(), "malformed": "{", "trailing": batch(event) + "{}",
		"unknown":              strings.Replace(batch(event), "current_rms_a", "current", 1),
		"duplicate":            strings.Replace(batch(event), `"schema_version":1`, `"schema_version":2,"schema_version":1`, 1),
		"case":                 strings.Replace(batch(event), "received_at", "RECEIVED_AT", 1),
		"too many":             `{"events":[` + strings.Repeat(event+",", 1000) + event + `]}`,
		"bad device":           strings.Replace(batch(event), "ce005098", "--------", 1),
		"bad machine":          strings.Replace(batch(event), "fecbfd44", "wrong", 1),
		"mismatched event":     strings.Replace(batch(event), "c89ad15e", "aaaaaaaa", 1),
		"bad received":         strings.Replace(batch(event), "2026-10-01", "yesterday", 1),
		"missing observed":     strings.Replace(batch(event), `"2026-10-01T08:03:00.112233+03:00"`, "null", 1),
		"schema":               strings.Replace(batch(event), `"schema_version":1`, `"schema_version":2`, 1),
		"state":                strings.Replace(batch(event), "running", "paused", 1),
		"missing vibration":    strings.Replace(batch(event), `"vibration_rms_g":0,`, "", 1),
		"null vibration":       strings.Replace(batch(event), `"vibration_rms_g":0`, `"vibration_rms_g":null`, 1),
		"negative frequency":   strings.Replace(batch(event), "null", "-1", 1),
		"negative vibration":   strings.Replace(batch(event), `"vibration_rms_g":0`, `"vibration_rms_g":-1`, 1),
		"out of storage range": strings.Replace(batch(event), "2026-10-01", "9999-10-01", 1),
		"invalid second event": batch(event, strings.Replace(event, "2.5", "-1", 1)),
	} {
		t.Run(name, func(t *testing.T) {
			status, _ := post(t, srv, body, bearer)
			assert.Equal(t, http.StatusBadRequest, status)
		})
	}
	body := batch(event)
	status, _ := post(t, srv, body+strings.Repeat(" ", (16<<20)-len(body)+1), bearer)
	assert.Equal(t, http.StatusRequestEntityTooLarge, status)
}
