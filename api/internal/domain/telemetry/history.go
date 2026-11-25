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
	DeviceID       string    `json:"device_id"       validate:"required" format:"uuid"`
	EventID        string    `json:"event_id"        validate:"required" format:"uuid"`
	ReceivedAt     time.Time `json:"received_at"     validate:"required" format:"date-time"`
	SchemaVersion  uint8     `json:"schema_version"  validate:"required"                    enums:"1"`
	ObservedAt     time.Time `json:"observed_at"     validate:"required" format:"date-time"`
	OperatingState string    `json:"operating_state" validate:"required"                    enums:"running,unknown"`
	// RMS vibration in g.
	VibrationRMSG float64 `json:"vibration_rms_g" validate:"required" format:"double" minimum:"0"`
	// Dominant frequency in Hz, or null when absent.
	DominantFrequencyHz *float64 `json:"dominant_frequency_hz" validate:"required" format:"double" minimum:"0" extensions:"x-nullable"`
	// RMS current in amperes, or null when absent.
	CurrentRMSA *float64 `json:"current_rms_a" validate:"required" format:"double" minimum:"0" extensions:"x-nullable"`
} // @name HistoryItem

type HistoryPage struct {
	Items []HistoryItem `json:"items" validate:"required"`
	// Opaque cursor for the next page, or null at the end of the history.
	NextCursor *string `json:"next_cursor" validate:"required" maxLength:"512" extensions:"x-nullable"`
} // @name HistoryPage
