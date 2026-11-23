package telemetry

import (
	"encoding/json"
	"errors"
	"time"
)

const (
	OutboxAnalyticsHTTP      = "analytics_http"
	OutboxAnalyticsTimeout   = "analytics_timeout"
	OutboxAnalyticsTransport = "analytics_transport"
	OutboxAnalyticsRequest   = "analytics_request"
)

var (
	ErrAnalyticsHTTP      = errors.New("analytics returned an unsuccessful response")
	ErrAnalyticsTimeout   = errors.New("analytics request timed out")
	ErrAnalyticsTransport = errors.New("analytics request failed")
	ErrAnalyticsRequest   = errors.New("analytics request could not be encoded or created")
)

type EventKey struct {
	DeviceID string
	EventID  string
}

type OutboxEvent struct {
	DeviceID     string          `json:"device_id"`
	EventID      string          `json:"event_id"`
	MachineID    string          `json:"machine_id"`
	ReceivedAt   time.Time       `json:"received_at"`
	Payload      json.RawMessage `json:"payload"`
	AttemptCount int64           `json:"-"`
}
