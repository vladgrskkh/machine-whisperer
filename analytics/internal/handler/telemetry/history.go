package telemetry

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/vladgrskkh/machine-whisperer/analytics/internal/domain"
)

const (
	defaultHistoryLimit = 100
	maxHistoryLimit     = 1000
	maxCursorLength     = 512
	machineIDParam      = "machine_id"
)

var errInvalidHistoryQuery = errors.New("invalid telemetry history query")

type HistoryLister interface {
	Execute(ctx context.Context, query domain.HistoryQuery) (domain.HistoryPage, error)
}

type historyItem struct {
	DeviceID            string    `json:"device_id"`
	EventID             string    `json:"event_id"`
	ReceivedAt          time.Time `json:"received_at"`
	SchemaVersion       uint8     `json:"schema_version"`
	ObservedAt          time.Time `json:"observed_at"`
	OperatingState      string    `json:"operating_state"`
	VibrationRMSG       float64   `json:"vibration_rms_g"`
	DominantFrequencyHz *float64  `json:"dominant_frequency_hz"`
	CurrentRMSA         *float64  `json:"current_rms_a"`
}

type historyResponse struct {
	Items      []historyItem `json:"items"`
	NextCursor *string       `json:"next_cursor"`
}

func NewHistory(list HistoryLister) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		query, err := readHistoryQuery(r)
		if err != nil {
			http.Error(w, errInvalidHistoryQuery.Error(), http.StatusBadRequest)
			return
		}
		page, err := list.Execute(r.Context(), query)
		if err != nil {
			http.Error(w, "telemetry storage unavailable", http.StatusServiceUnavailable)
			return
		}
		writeHistory(w, page)
	})
}

func readHistoryQuery(r *http.Request) (domain.HistoryQuery, error) {
	rawID := chi.URLParam(r, machineIDParam)
	machineID, err := uuid.Parse(rawID)
	if err != nil || len(rawID) != len(machineID.String()) {
		return domain.HistoryQuery{}, errInvalidHistoryQuery
	}

	values, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil || len(values["limit"]) > 1 || len(values["cursor"]) > 1 {
		return domain.HistoryQuery{}, errInvalidHistoryQuery
	}
	query := domain.HistoryQuery{MachineID: machineID.String(), Limit: defaultHistoryLimit}
	if values.Has("limit") {
		query.Limit, err = strconv.Atoi(values.Get("limit"))
		if err != nil || query.Limit < 1 || query.Limit > maxHistoryLimit {
			return domain.HistoryQuery{}, errInvalidHistoryQuery
		}
	}
	if values.Has("cursor") {
		query.Cursor, err = readHistoryCursor(values.Get("cursor"))
		if err != nil || query.Cursor.MachineID != query.MachineID {
			return domain.HistoryQuery{}, errInvalidHistoryQuery
		}
	}
	return query, nil
}

func readHistoryCursor(token string) (*domain.HistoryCursor, error) {
	if len(token) > maxCursorLength || strings.ContainsAny(token, "\r\n") {
		return nil, errInvalidHistoryQuery
	}
	data, err := base64.RawURLEncoding.Strict().DecodeString(token)
	if err != nil {
		return nil, errInvalidHistoryQuery
	}
	var cursor domain.HistoryCursor
	var observedAt string
	dec := json.NewDecoder(bytes.NewReader(data))
	err = object(dec, map[string]any{
		machineIDParam: &cursor.MachineID,
		"observed_at":  &observedAt,
		"device_id":    &cursor.DeviceID,
		eventIDField:   &cursor.EventID,
	})
	if err != nil {
		return nil, errInvalidHistoryQuery
	}
	if _, err = dec.Token(); !errors.Is(err, io.EOF) {
		return nil, errInvalidHistoryQuery
	}
	cursor.ObservedAt, err = parseTimestamp(observedAt)
	if err != nil || !cursor.Valid() {
		return nil, errInvalidHistoryQuery
	}
	cursor.ObservedAt = cursor.ObservedAt.UTC().Truncate(time.Microsecond)
	return &cursor, nil
}

func writeHistory(w http.ResponseWriter, page domain.HistoryPage) {
	response := historyResponse{Items: make([]historyItem, 0, len(page.Events)), NextCursor: page.NextCursor}
	for _, event := range page.Events {
		p := event.Payload
		response.Items = append(response.Items, historyItem{
			DeviceID: event.DeviceID, EventID: event.EventID, ReceivedAt: event.ReceivedAt,
			SchemaVersion: p.SchemaVersion, ObservedAt: p.ObservedAt, OperatingState: p.OperatingState,
			VibrationRMSG: p.VibrationRMSG, DominantFrequencyHz: p.DominantFrequencyHz, CurrentRMSA: p.CurrentRMSA,
		})
	}
	body, err := json.Marshal(response)
	if err != nil {
		http.Error(w, "telemetry storage unavailable", http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(body)
}
