package telemetry

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/google/uuid"

	"github.com/vladgrskkh/machine-whisperer/api/internal/domain/machine"
	"github.com/vladgrskkh/machine-whisperer/api/internal/domain/telemetry"
)

const (
	maxFutureSkew = 5 * time.Minute
	uuidTextBytes = 36
)

var (
	ErrInvalidObservation = errors.New("invalid telemetry observation")
	ErrObservationWindow  = errors.New("observation outside telemetry acceptance window")
	ErrObservationOrder   = errors.New("observation precedes machine telemetry cursor")
)

type Repository interface {
	LockActiveDeviceForTelemetry(ctx context.Context, deviceID, machineID string) error
	LockMachineForTelemetry(ctx context.Context, machineID string) (*time.Time, time.Time, error)
	DeleteExpiredTelemetryEvents(ctx context.Context, machineID, deviceID, eventID string, dbNow time.Time) error
	FindTelemetryEvent(ctx context.Context, deviceID, eventID string) ([32]byte, bool, error)
	InsertTelemetryEvent(ctx context.Context, event telemetry.Event) error
	InsertTelemetryOutbox(ctx context.Context, event telemetry.Event) error
	UpdateMachineTelemetryCursor(ctx context.Context, machineID string, observedAt time.Time) error
}

type TransactionManager interface {
	WithinTransaction(context.Context, func(context.Context) error) error
}

type ObservationDetector interface {
	Observe(context.Context, telemetry.Event) (Detection, error)
}

type IngestRecorder interface {
	RecordIngest(IngestOutcome)
	RecordDetection(DetectorOutcome)
	RecordAlertTransition(AlertTransition)
}

type IngestOutcome string

const (
	IngestAcceptedNew    IngestOutcome = "accepted_new"
	IngestAcceptedReplay IngestOutcome = "accepted_replay"
	IngestInvalid        IngestOutcome = "invalid"
	IngestMachineMissing IngestOutcome = "machine_not_found"
	IngestConflict       IngestOutcome = "conflict"
	IngestOutsideWindow  IngestOutcome = "outside_window"
	IngestOutOfOrder     IngestOutcome = "out_of_order"
	IngestInternalError  IngestOutcome = "internal_error"
)

type ingestResult struct {
	replay    bool
	detection Detection
}

type Ingest struct {
	repository   Repository
	transactions TransactionManager
	detector     ObservationDetector
	recorder     IngestRecorder
}

func NewIngest(
	repository Repository,
	transactions TransactionManager,
	detector ObservationDetector,
	recorder IngestRecorder,
) *Ingest {
	return &Ingest{repository: repository, transactions: transactions, detector: detector, recorder: recorder}
}

func (i *Ingest) Ingest(
	ctx context.Context,
	deviceID, assignedMachineID, requestedMachineID string,
	observation telemetry.Observation,
) error {
	if assignedMachineID == "" || assignedMachineID != requestedMachineID {
		i.recorder.RecordIngest(IngestMachineMissing)
		return machine.ErrNotFound
	}
	normalized, err := normalizeObservation(observation)
	if err != nil {
		i.recorder.RecordIngest(IngestInvalid)
		return err
	}
	event := telemetry.Event{
		Observation: normalized,
		DeviceID:    deviceID,
		MachineID:   requestedMachineID,
	}
	var result ingestResult
	err = i.transactions.WithinTransaction(ctx, func(txCtx context.Context) error {
		var storeErr error
		result, storeErr = i.store(txCtx, event)
		return storeErr
	})
	if err != nil {
		i.recorder.RecordIngest(storeFailureOutcome(err))
		return err
	}
	if result.replay {
		i.recorder.RecordIngest(IngestAcceptedReplay)
		return nil
	}
	i.recorder.RecordIngest(IngestAcceptedNew)
	i.recorder.RecordDetection(result.detection.Outcome)
	if result.detection.Transition != "" {
		i.recorder.RecordAlertTransition(result.detection.Transition)
	}
	return nil
}

func (i *Ingest) store(ctx context.Context, event telemetry.Event) (ingestResult, error) {
	if err := i.repository.LockActiveDeviceForTelemetry(ctx, event.DeviceID, event.MachineID); err != nil {
		return ingestResult{}, err
	}
	cursor, dbNow, err := i.repository.LockMachineForTelemetry(ctx, event.MachineID)
	if err != nil {
		return ingestResult{}, err
	}
	event.ReceivedAt = dbNow
	if !event.ObservedAt.After(dbNow.Add(-telemetry.RetryWindow)) || event.ObservedAt.After(dbNow.Add(maxFutureSkew)) {
		return ingestResult{}, ErrObservationWindow
	}
	payload, err := json.Marshal(event.Observation)
	if err != nil {
		return ingestResult{}, fmt.Errorf("encode normalized telemetry observation: %w", err)
	}
	event.PayloadHash = sha256.Sum256(payload)
	if err = i.repository.DeleteExpiredTelemetryEvents(
		ctx,
		event.MachineID,
		event.DeviceID,
		event.EventID,
		dbNow,
	); err != nil {
		return ingestResult{}, err
	}

	hash, found, err := i.repository.FindTelemetryEvent(ctx, event.DeviceID, event.EventID)
	if err != nil {
		return ingestResult{}, err
	}
	if found {
		if hash == event.PayloadHash {
			return ingestResult{replay: true}, nil
		}
		return ingestResult{}, telemetry.ErrEventConflict
	}
	if cursor != nil && event.ObservedAt.Before(*cursor) {
		return ingestResult{}, ErrObservationOrder
	}
	if err = i.repository.InsertTelemetryEvent(ctx, event); err != nil {
		return ingestResult{}, err
	}
	if err = i.repository.InsertTelemetryOutbox(ctx, event); err != nil {
		return ingestResult{}, err
	}
	if err = i.repository.UpdateMachineTelemetryCursor(ctx, event.MachineID, event.ObservedAt); err != nil {
		return ingestResult{}, err
	}
	detection, err := i.detector.Observe(ctx, event)
	return ingestResult{detection: detection}, err
}

func storeFailureOutcome(err error) IngestOutcome {
	switch {
	case errors.Is(err, machine.ErrNotFound):
		return IngestMachineMissing
	case errors.Is(err, telemetry.ErrEventConflict):
		return IngestConflict
	case errors.Is(err, ErrObservationWindow):
		return IngestOutsideWindow
	case errors.Is(err, ErrObservationOrder):
		return IngestOutOfOrder
	default:
		return IngestInternalError
	}
}

func normalizeObservation(value telemetry.Observation) (telemetry.Observation, error) {
	eventID, err := uuid.Parse(value.EventID)
	if err != nil || len(value.EventID) != uuidTextBytes || value.SchemaVersion != 1 ||
		(value.OperatingState != "running" && value.OperatingState != "unknown") ||
		!validReading(value.VibrationRMSG) {
		return telemetry.Observation{}, ErrInvalidObservation
	}
	var ok bool
	if value.DominantFrequencyHz, ok = normalizeOptionalReading(value.DominantFrequencyHz); !ok {
		return telemetry.Observation{}, ErrInvalidObservation
	}
	if value.CurrentRMSA, ok = normalizeOptionalReading(value.CurrentRMSA); !ok {
		return telemetry.Observation{}, ErrInvalidObservation
	}
	value.EventID = eventID.String()
	value.ObservedAt = value.ObservedAt.UTC().Truncate(time.Microsecond)
	// Signed zero compares equal as a float64 and must hash identically.
	value.VibrationRMSG = math.Abs(value.VibrationRMSG)
	return value, nil
}

func validReading(value float64) bool {
	return value >= 0 && !math.IsInf(value, 1)
}

func normalizeOptionalReading(value *float64) (*float64, bool) {
	if value == nil {
		return nil, true
	}
	if !validReading(*value) {
		return nil, false
	}
	normalized := math.Abs(*value)
	return &normalized, true
}
