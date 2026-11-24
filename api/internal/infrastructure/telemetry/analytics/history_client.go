package analytics

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/vladgrskkh/machine-whisperer/api/internal/domain/telemetry"
)

const (
	historyTimeout      = 10 * time.Second
	maxHistoryPageBytes = 1024 * 1024
)

type HistoryClient struct {
	baseURL string
	apiKey  string
	client  *http.Client
}

func NewHistoryClient(baseURL, apiKey string) *HistoryClient {
	return &HistoryClient{
		baseURL: strings.TrimRight(baseURL, "/"),
		apiKey:  apiKey,
		client: &http.Client{
			Timeout: historyTimeout,
			CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
	}
}

func (c *HistoryClient) List(
	ctx context.Context,
	machineID string,
	limit int,
	cursor string,
) (telemetry.HistoryPage, error) {
	if c.baseURL == "" {
		return telemetry.HistoryPage{}, telemetry.ErrHistoryUnavailable
	}
	values := url.Values{"limit": {strconv.Itoa(limit)}}
	if cursor != "" {
		values.Set("cursor", cursor)
	}
	endpoint := c.baseURL + "/internal/v1/machines/" + url.PathEscape(machineID) + "/telemetry?" + values.Encode()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return telemetry.HistoryPage{}, telemetry.ErrHistoryUnavailable
	}
	request.Header.Set("Authorization", "Bearer "+c.apiKey)
	response, err := c.client.Do(request)
	if err != nil {
		return telemetry.HistoryPage{}, telemetry.ErrHistoryUnavailable
	}
	defer func() { _ = response.Body.Close() }()
	switch response.StatusCode {
	case http.StatusBadRequest:
		return telemetry.HistoryPage{}, telemetry.ErrInvalidHistoryQuery
	case http.StatusOK:
		return readHistoryPage(response.Body, limit)
	default:
		return telemetry.HistoryPage{}, telemetry.ErrHistoryUnavailable
	}
}

func readHistoryPage(body io.Reader, limit int) (telemetry.HistoryPage, error) {
	raw, err := io.ReadAll(io.LimitReader(body, maxHistoryPageBytes+1))
	if err != nil || len(raw) > maxHistoryPageBytes {
		return telemetry.HistoryPage{}, telemetry.ErrHistoryUnavailable
	}
	var page telemetry.HistoryPage
	if err = json.Unmarshal(raw, &page); err != nil || page.Items == nil || len(page.Items) > limit {
		return telemetry.HistoryPage{}, telemetry.ErrHistoryUnavailable
	}
	return page, nil
}
