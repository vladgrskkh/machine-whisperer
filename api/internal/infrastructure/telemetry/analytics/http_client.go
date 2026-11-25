package analytics

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/vladgrskkh/machine-whisperer/api/internal/domain/telemetry"
)

const BatchPath = "/internal/v1/telemetry-batches"

const (
	SendSuccess  = "success"
	SendCanceled = "canceled"
)

type SendRecorder interface {
	RecordSend(outcome string, duration time.Duration)
}

type HTTPClient struct {
	url      string
	apiKey   string
	client   *http.Client
	recorder SendRecorder
}

func NewHTTPClient(baseURL, apiKey string, timeout time.Duration, recorder SendRecorder) *HTTPClient {
	return &HTTPClient{
		url:      strings.TrimRight(baseURL, "/") + BatchPath,
		apiKey:   apiKey,
		recorder: recorder,
		client: &http.Client{
			Timeout: timeout,
			CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
				// Only the configured endpoint can acknowledge delivery.
				return http.ErrUseLastResponse
			},
		},
	}
}

func (c *HTTPClient) Send(ctx context.Context, events []telemetry.OutboxEvent) error {
	started := time.Now()
	err := c.send(ctx, events)
	c.recorder.RecordSend(sendOutcome(err), time.Since(started))
	return err
}

func (c *HTTPClient) send(ctx context.Context, events []telemetry.OutboxEvent) error {
	body, err := json.Marshal(struct {
		Events []telemetry.OutboxEvent `json:"events"`
	}{Events: events})
	if err != nil {
		return telemetry.ErrAnalyticsRequest
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, c.url, bytes.NewReader(body))
	if err != nil {
		return telemetry.ErrAnalyticsRequest
	}
	request.Header.Set("Authorization", "Bearer "+c.apiKey)
	request.Header.Set("Content-Type", "application/json")
	response, err := c.client.Do(request)
	if err != nil {
		if errors.Is(err, context.Canceled) {
			return errors.Join(telemetry.ErrAnalyticsTransport, context.Canceled)
		}
		if errors.Is(err, context.DeadlineExceeded) {
			return telemetry.ErrAnalyticsTimeout
		}
		return telemetry.ErrAnalyticsTransport
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return telemetry.ErrAnalyticsHTTP
	}
	return nil
}

func sendOutcome(err error) string {
	switch {
	case err == nil:
		return SendSuccess
	case errors.Is(err, context.Canceled):
		return SendCanceled
	default:
		return telemetry.AnalyticsFailureCategory(err)
	}
}
