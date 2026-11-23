package alert

import (
	"errors"
	"time"
)

var ErrNotFound = errors.New("alert not found")

type Alert struct {
	ID                      string
	MachineID               string
	TriggerDeviceID         string
	TriggerEventID          string
	TriggeredAt             time.Time
	TriggerZScore           float64
	TriggerBaselineVersion  int64
	TriggerMeanRMSG         float64
	TriggerStddevRMSG       float64
	LastZScore              float64
	PeakZScore              float64
	RecoveryDeviceID        *string
	RecoveryEventID         *string
	RecoveredAt             *time.Time
	RecoveryBaselineVersion *int64
	AcknowledgedAt          *time.Time
}
