package machine

import (
	"errors"
	"math"
	"time"
)

var (
	ErrInvalidBaseline  = errors.New("invalid machine baseline")
	ErrBaselineNotFound = errors.New("machine baseline not found")
)

type Baseline struct {
	MachineID  string
	MeanRMSG   float64
	StddevRMSG float64
	Version    int64
	UpdatedAt  time.Time
}

func NewBaseline(machineID string, meanRMSG, stddevRMSG float64) (Baseline, error) {
	if !(meanRMSG >= 0 && meanRMSG < math.Inf(1) && stddevRMSG > 0 && stddevRMSG < math.Inf(1)) {
		return Baseline{}, ErrInvalidBaseline
	}
	return Baseline{MachineID: machineID, MeanRMSG: math.Abs(meanRMSG), StddevRMSG: stddevRMSG}, nil
}
