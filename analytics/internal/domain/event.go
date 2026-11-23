package domain

import (
	"math"
	"time"

	"github.com/google/uuid"
)

type Event struct {
	DeviceID   string
	EventID    string
	MachineID  string
	ReceivedAt time.Time
	Payload    Observation
}

type Observation struct {
	SchemaVersion       uint8
	EventID             string
	ObservedAt          time.Time
	OperatingState      string
	VibrationRMSG       float64
	DominantFrequencyHz *float64
	CurrentRMSA         *float64
}

func (e Event) Valid() bool {
	p := e.Payload
	return validID(e.DeviceID) && validID(e.MachineID) && validID(e.EventID) && e.EventID == p.EventID &&
		validTime(e.ReceivedAt) && validTime(p.ObservedAt) && p.SchemaVersion == 1 &&
		(p.OperatingState == "running" || p.OperatingState == "unknown") && p.VibrationRMSG >= 0 &&
		(p.DominantFrequencyHz == nil || *p.DominantFrequencyHz >= 0) && (p.CurrentRMSA == nil || *p.CurrentRMSA >= 0)
}

func validID(id string) bool {
	const canonicalLength = 36
	return len(id) == canonicalLength && uuid.Validate(id) == nil
}

func validTime(value time.Time) bool {
	// ClickHouse DateTime64 starts in 1900. The native driver converts through UnixNano.
	const firstYear = 1900
	return value.UTC().Year() >= firstYear && !value.After(time.Unix(0, math.MaxInt64))
}
