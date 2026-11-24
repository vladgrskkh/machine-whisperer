package telemetry

import (
	"errors"
	"time"
)

var (
	ErrInvalidHistoryQuery = errors.New("invalid telemetry history query")
	ErrHistoryUnavailable  = errors.New("telemetry history unavailable")
)

type HistoryItem struct {
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

type HistoryPage struct {
	Items      []HistoryItem `json:"items"`
	NextCursor *string       `json:"next_cursor"`
}
