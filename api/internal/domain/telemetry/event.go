package telemetry

import (
	"errors"
	"time"
)

const RetryWindow = 30 * 24 * time.Hour

var ErrEventConflict = errors.New("telemetry event ID conflict")

type Observation struct {
	SchemaVersion int    `json:"schema_version" validate:"required" enums:"1"`
	EventID       string `json:"event_id"       validate:"required"           format:"uuid" example:"77777777-7777-4777-8777-777777777777"`
	// Observation timestamp in RFC 3339.
	ObservedAt     time.Time `json:"observed_at"     validate:"required" format:"date-time"`
	OperatingState string    `json:"operating_state" validate:"required"                    enums:"running,unknown"`
	// Finite, nonnegative RMS vibration in g.
	VibrationRMSG float64 `json:"vibration_rms_g" validate:"required" format:"double" minimum:"0"`
	// Finite, nonnegative dominant frequency in Hz when supplied. May be omitted or null.
	DominantFrequencyHz *float64 `json:"dominant_frequency_hz" format:"double" minimum:"0" extensions:"x-nullable"`
	// Finite, nonnegative RMS current in amperes when supplied. May be omitted or null.
	CurrentRMSA *float64 `json:"current_rms_a" format:"double" minimum:"0" extensions:"x-nullable"`
} // @name Observation

type Event struct {
	Observation

	DeviceID    string
	MachineID   string
	ReceivedAt  time.Time
	PayloadHash [32]byte
}
