package telemetry

import (
	"errors"
	"time"
)

const RetryWindow = 30 * 24 * time.Hour

var ErrEventConflict = errors.New("telemetry event ID conflict")

type Observation struct {
	SchemaVersion       int       `json:"schema_version"`
	EventID             string    `json:"event_id"`
	ObservedAt          time.Time `json:"observed_at"`
	OperatingState      string    `json:"operating_state"`
	VibrationRMSG       float64   `json:"vibration_rms_g"`
	DominantFrequencyHz *float64  `json:"dominant_frequency_hz"`
	CurrentRMSA         *float64  `json:"current_rms_a"`
}

type Event struct {
	Observation

	DeviceID    string
	MachineID   string
	ReceivedAt  time.Time
	PayloadHash [32]byte
}
