package application_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/vladgrskkh/machine-whisperer/analytics/internal/application"
	"github.com/vladgrskkh/machine-whisperer/analytics/internal/config"
	"github.com/vladgrskkh/machine-whisperer/analytics/internal/domain"
)

const (
	ingestKey = "relay-key"
	bearer    = "Bearer " + ingestKey
	eventID   = "c89ad15e-4cff-4364-b959-ac185e98c511"
	batchBody = `{"events":[{"device_id":"ce005098-0257-4cbe-9d08-414068d84722","event_id":"` + eventID +
		`","machine_id":"fecbfd44-b504-472d-ac63-eebdbb96dfc7","received_at":"2026-10-01T05:04:03Z",` +
		`"payload":{"schema_version":1,"event_id":"` + eventID +
		`","observed_at":"2026-10-01T05:03:00Z","operating_state":"running","vibration_rms_g":0}}]}`
)

func TestBatchRoutePersistsAnAuthenticatedBatch(t *testing.T) {
	repository := NewMockTelemetryRepository(t)
	repository.On("StoreBatch", mock.Anything, mock.MatchedBy(func(events []domain.Event) bool {
		return len(events) == 1 && events[0].EventID == eventID
	})).Return(nil).Once()
	srv := newServer(t, repository)

	status := post(t, srv, batchBody, bearer)

	assert.Equal(t, http.StatusNoContent, status)
}

func TestBatchRouteRejectsInvalidCredentialsBeforePersistence(t *testing.T) {
	srv := newServer(t, NewMockTelemetryRepository(t))
	for _, headers := range [][]string{nil, {"Bearer wrong"}, {"Basic " + ingestKey}, {bearer, bearer}} {
		assert.Equal(t, http.StatusUnauthorized, post(t, srv, batchBody, headers...))
	}
}

func TestBatchRouteRejectsAnInvalidBatchBeforePersistence(t *testing.T) {
	srv := newServer(t, NewMockTelemetryRepository(t))
	assert.Equal(t, http.StatusBadRequest, post(t, srv, `{"events":[]}`, bearer))
}

func newServer(t *testing.T, repository *MockTelemetryRepository) *httptest.Server {
	t.Helper()
	app, err := application.New(
		t.Context(),
		config.Config{IngestKey: ingestKey},
		application.WithTelemetryRepository(repository),
	)
	require.NoError(t, err)
	t.Cleanup(app.Close)
	srv := httptest.NewServer(app.Routes())
	t.Cleanup(srv.Close)
	return srv
}

func post(t *testing.T, srv *httptest.Server, body string, headers ...string) int {
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
	return resp.StatusCode
}
