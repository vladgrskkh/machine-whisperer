package telemetry

import (
	"context"
	"math"

	"github.com/vladgrskkh/machine-whisperer/api/internal/domain/alert"
	"github.com/vladgrskkh/machine-whisperer/api/internal/domain/machine"
	"github.com/vladgrskkh/machine-whisperer/api/internal/domain/telemetry"
)

type DetectorState struct {
	BaselineVersion      int64
	PendingAlertCount    int
	PendingRecoveryCount int

	// Active is loaded from alert history rather than stored with the counts.
	Active *ActiveAlert
}

type ActiveAlert struct {
	ID         string
	PeakZScore float64
}

type DetectionRepository interface {
	Load(ctx context.Context, machineID string) (machine.Baseline, DetectorState, bool, error)
	SaveTransition(ctx context.Context, machineID string, state DetectorState, changedAlert *alert.Alert) error
}

type DetectorSettings struct {
	AlertZThreshold               float64
	RecoveryZThreshold            float64
	AlertSustainedObservations    int
	RecoverySustainedObservations int
}

type DetectorOutcome string

const (
	DetectorEvaluated    DetectorOutcome = "evaluated"
	DetectorNoBaseline   DetectorOutcome = "no_baseline"
	DetectorUnknownState DetectorOutcome = "unknown_state"
)

type AlertTransition string

const (
	AlertOpened    AlertTransition = "opened"
	AlertRecovered AlertTransition = "recovered"
)

type Detection struct {
	Outcome    DetectorOutcome
	Transition AlertTransition
}

type Detector struct {
	repository DetectionRepository
	settings   DetectorSettings
}

func NewDetector(repository DetectionRepository, settings DetectorSettings) *Detector {
	return &Detector{repository: repository, settings: settings}
}

func (d *Detector) Observe(ctx context.Context, event telemetry.Event) (Detection, error) {
	baseline, state, exists, err := d.repository.Load(ctx, event.MachineID)
	if err != nil {
		return Detection{}, err
	}
	if !exists {
		return Detection{Outcome: DetectorNoBaseline}, nil
	}

	previous := state
	result := Detection{Outcome: DetectorEvaluated}
	if state.BaselineVersion != baseline.Version || event.OperatingState == "unknown" {
		state.BaselineVersion = baseline.Version
		state.PendingAlertCount = 0
		state.PendingRecoveryCount = 0
	}
	var changedAlert *alert.Alert
	if event.OperatingState == "running" {
		z := math.Abs(event.VibrationRMSG-baseline.MeanRMSG) / baseline.StddevRMSG
		if state.Active == nil {
			changedAlert = d.detectAlert(event, baseline, &state, z)
		} else {
			changedAlert = d.updateAlert(event, baseline, &state, z)
		}
	} else {
		result.Outcome = DetectorUnknownState
	}
	if state == previous && changedAlert == nil {
		return result, nil
	}
	if err := d.repository.SaveTransition(ctx, event.MachineID, state, changedAlert); err != nil {
		return Detection{}, err
	}
	if changedAlert != nil {
		switch {
		case changedAlert.ID == "":
			result.Transition = AlertOpened
		case changedAlert.RecoveredAt != nil:
			result.Transition = AlertRecovered
		}
	}
	return result, nil
}

func (d *Detector) detectAlert(
	event telemetry.Event, baseline machine.Baseline, state *DetectorState, z float64,
) *alert.Alert {
	if z < d.settings.AlertZThreshold {
		state.PendingAlertCount = 0
		return nil
	}
	state.PendingAlertCount++
	if state.PendingAlertCount < d.settings.AlertSustainedObservations {
		return nil
	}
	state.PendingAlertCount = 0
	// Valid inputs can produce an overflowing score. Keep stored scores JSON-encodable.
	z = min(z, math.MaxFloat64)
	return &alert.Alert{
		MachineID:              event.MachineID,
		TriggerDeviceID:        event.DeviceID,
		TriggerEventID:         event.EventID,
		TriggeredAt:            event.ObservedAt,
		TriggerZScore:          z,
		TriggerBaselineVersion: baseline.Version,
		TriggerMeanRMSG:        baseline.MeanRMSG,
		TriggerStddevRMSG:      baseline.StddevRMSG,
		LastZScore:             z,
		PeakZScore:             z,
	}
}

func (d *Detector) updateAlert(
	event telemetry.Event, baseline machine.Baseline, state *DetectorState, z float64,
) *alert.Alert {
	last := min(z, math.MaxFloat64)
	changed := &alert.Alert{
		ID:         state.Active.ID,
		MachineID:  event.MachineID,
		LastZScore: last,
		PeakZScore: max(last, state.Active.PeakZScore),
	}
	// Division can round a nonzero deviation to zero.
	threshold := d.settings.RecoveryZThreshold
	if z > threshold || (threshold == 0 && event.VibrationRMSG != baseline.MeanRMSG) {
		state.PendingRecoveryCount = 0
		return changed
	}
	state.PendingRecoveryCount++
	if state.PendingRecoveryCount >= d.settings.RecoverySustainedObservations {
		state.PendingRecoveryCount = 0
		changed.RecoveryDeviceID = &event.DeviceID
		changed.RecoveryEventID = &event.EventID
		changed.RecoveredAt = &event.ObservedAt
		changed.RecoveryBaselineVersion = &state.BaselineVersion
	}
	return changed
}
