package domain

import (
	"strings"
	"time"
)

type HistoryCursor struct {
	MachineID  string    `json:"machine_id"`
	ObservedAt time.Time `json:"observed_at"`
	DeviceID   string    `json:"device_id"`
	EventID    string    `json:"event_id"`
}

func (c HistoryCursor) Valid() bool {
	return validTime(c.ObservedAt) && canonicalID(c.MachineID) && canonicalID(c.DeviceID) && canonicalID(c.EventID)
}

func canonicalID(id string) bool {
	return validID(id) && id == strings.ToLower(id)
}

type HistoryQuery struct {
	MachineID string
	Limit     int
	Cursor    *HistoryCursor
}

type HistoryPage struct {
	Events     []Event
	NextCursor *string
}
