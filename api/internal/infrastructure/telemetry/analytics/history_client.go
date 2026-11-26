package analytics

import (
	"context"
	"encoding/json"
	"io"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

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

func NewHistoryClient(baseURL, apiKey string, transport http.RoundTripper) *HistoryClient {
	return &HistoryClient{
		baseURL: strings.TrimRight(baseURL, "/"),
		apiKey:  apiKey,
		client: &http.Client{
			Timeout:   historyTimeout,
			Transport: transport,
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
	var wire struct {
		Items      []historyItem `json:"items"`
		NextCursor *string       `json:"next_cursor"`
	}
	if err = json.Unmarshal(raw, &wire); err != nil || wire.Items == nil || len(wire.Items) > limit {
		return telemetry.HistoryPage{}, telemetry.ErrHistoryUnavailable
	}
	page := telemetry.HistoryPage{
		Items:      make([]telemetry.HistoryItem, 0, len(wire.Items)),
		NextCursor: wire.NextCursor,
	}
	for _, item := range wire.Items {
		if !item.valid() {
			return telemetry.HistoryPage{}, telemetry.ErrHistoryUnavailable
		}
		item.HistoryItem.VibrationRMSG = *item.VibrationRMS
		page.Items = append(page.Items, item.HistoryItem)
	}
	return page, nil
}

type historyItem struct {
	telemetry.HistoryItem

	// A valid zero vibration reading must be distinguishable from a missing or null field.
	VibrationRMS *float64 `json:"vibration_rms_g"`
}

func (i historyItem) valid() bool {
	return i.SchemaVersion == 1 && (i.OperatingState == "running" || i.OperatingState == "unknown") &&
		canonicalHistoryID(i.DeviceID) && canonicalHistoryID(i.EventID) &&
		validHistoryTimestamp(i.ReceivedAt) && validHistoryTimestamp(i.ObservedAt) &&
		i.VibrationRMS != nil && *i.VibrationRMS >= 0 &&
		(i.DominantFrequencyHz == nil || *i.DominantFrequencyHz >= 0) &&
		(i.CurrentRMSA == nil || *i.CurrentRMSA >= 0)
}

func canonicalHistoryID(value string) bool {
	id, err := uuid.Parse(value)
	return err == nil && id.String() == value
}

func validHistoryTimestamp(stamp time.Time) bool {
	// Analytics stores timestamps in DateTime64 and uses the driver's UnixNano conversion.
	const firstHistoryYear = 1900
	return stamp.UTC().Year() >= firstHistoryYear && !stamp.After(time.Unix(0, math.MaxInt64))
}
