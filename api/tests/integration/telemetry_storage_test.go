//go:build integration

package integration_test

import (
	"context"
	"crypto/sha256"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgerrcode"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vladgrskkh/machine-whisperer/api/internal/domain/machine"
	"github.com/vladgrskkh/machine-whisperer/api/internal/domain/telemetry"
	devicepostgres "github.com/vladgrskkh/machine-whisperer/api/internal/infrastructure/device/postgres"
	machinepostgres "github.com/vladgrskkh/machine-whisperer/api/internal/infrastructure/machine/postgres"
	telemetrypostgres "github.com/vladgrskkh/machine-whisperer/api/internal/infrastructure/telemetry/postgres"
	transactionpostgres "github.com/vladgrskkh/machine-whisperer/api/internal/infrastructure/transaction/postgres"
	"github.com/vladgrskkh/machine-whisperer/api/internal/testutil"
)

const insertTelemetryIdentity = `INSERT INTO telemetry_events (device_id,event_id,machine_id,observed_at,payload_hash,expires_at)
	VALUES ($1,$2,$3,$4,$5,$6)`

func TestTelemetryStorageConstraints(t *testing.T) {
	t.Parallel()
	pool := testutil.Postgres(t)
	event, target := telemetryStorageEvent(t, pool)
	deviceID, eventID, machineID := event.DeviceID, event.EventID, event.MachineID
	observedAt, payloadHash := event.ObservedAt, event.PayloadHash
	expiresAt := observedAt.AddDate(0, 0, 30)
	ctx := t.Context()

	insertIdentity := func(deviceID, eventID, machineID string, hash []byte) error {
		_, err := pool.Exec(ctx, insertTelemetryIdentity, deviceID, eventID, machineID, observedAt, hash, expiresAt)
		return err
	}
	require.NoError(t, insertIdentity(deviceID, eventID, machineID, payloadHash[:]))
	requirePGCode(t, insertIdentity(deviceID, eventID, machineID, payloadHash[:]), pgerrcode.UniqueViolation)
	requirePGCode(t, insertIdentity(deviceID, uuid.NewString(), machineID, payloadHash[:31]), pgerrcode.CheckViolation)
	requirePGCode(
		t,
		insertIdentity(uuid.NewString(), eventID, machineID, payloadHash[:]),
		pgerrcode.ForeignKeyViolation,
	)
	requirePGCode(
		t,
		insertIdentity(deviceID, uuid.NewString(), uuid.NewString(), payloadHash[:]),
		pgerrcode.ForeignKeyViolation,
	)

	const outbox = `INSERT INTO telemetry_outbox (device_id,event_id,machine_id,payload,created_at) VALUES ($1,$2,$3,'{}',$4)`
	_, err := pool.Exec(ctx, outbox, deviceID, eventID, machineID, observedAt)
	require.NoError(t, err)
	_, err = pool.Exec(ctx, outbox, deviceID, eventID, machineID, observedAt)
	requirePGCode(t, err, pgerrcode.UniqueViolation)
	otherDeviceID := createTelemetryStorageDevice(t, pool, target)
	require.NoError(t, insertIdentity(otherDeviceID, eventID, machineID, payloadHash[:]))
	_, err = pool.Exec(ctx, outbox, otherDeviceID, eventID, machineID, observedAt)
	require.NoError(t, err)
}

func TestTelemetryEventAndOutboxInsertAtomically(t *testing.T) {
	t.Parallel()
	pool := testutil.Postgres(t)
	event, _ := telemetryStorageEvent(t, pool)
	ctx := t.Context()
	repository := telemetrypostgres.New()
	manager := transactionpostgres.New(pool)
	require.NoError(t, manager.WithinTransaction(ctx, func(ctx context.Context) error {
		return repository.InsertTelemetryOutbox(ctx, event)
	}))

	err := manager.WithinTransaction(ctx, func(ctx context.Context) error {
		return storeTelemetryEvent(ctx, repository, event)
	})
	require.ErrorIs(t, err, telemetry.ErrEventConflict)
	var identities int
	var cursor *time.Time
	require.NoError(t, pool.QueryRow(ctx, `SELECT
		(SELECT count(*) FROM telemetry_events),
		(SELECT last_telemetry_observed_at FROM machines WHERE id=$1)`, event.MachineID).Scan(&identities, &cursor))
	assert.Zero(t, identities)
	assert.Nil(t, cursor)

	event.EventID = uuid.NewString()
	require.NoError(t, manager.WithinTransaction(ctx, func(ctx context.Context) error {
		return storeTelemetryEvent(ctx, repository, event)
	}))

	var payload, machineID string
	var createdAt time.Time
	var deliveredAt *time.Time
	require.NoError(t, pool.QueryRow(ctx, `SELECT payload::text,machine_id::text,created_at,delivered_at
		FROM telemetry_outbox WHERE device_id=$1 AND event_id=$2`, event.DeviceID, event.EventID).
		Scan(&payload, &machineID, &createdAt, &deliveredAt))
	assert.JSONEq(t, fmt.Sprintf(`{
		"schema_version":1,"event_id":%q,"observed_at":"2026-11-23T12:00:00Z",
		"operating_state":"running","vibration_rms_g":0.043,"dominant_frequency_hz":39.1,"current_rms_a":null
	}`, event.EventID), payload)
	assert.Equal(t, event.MachineID, machineID)
	assert.WithinDuration(t, event.ReceivedAt, createdAt, 0)
	assert.Nil(t, deliveredAt)

	assertTelemetryIdentity(t, pool, event)
	err = manager.WithinTransaction(ctx, func(ctx context.Context) error {
		return repository.InsertTelemetryEvent(ctx, event)
	})
	require.ErrorIs(t, err, telemetry.ErrEventConflict)
}

func TestTelemetryMachineCursor(t *testing.T) {
	t.Parallel()
	pool := testutil.Postgres(t)
	event, target := telemetryStorageEvent(t, pool)
	repository := telemetrypostgres.New()
	manager := transactionpostgres.New(pool)
	ctx := t.Context()

	var before time.Time
	require.NoError(t, pool.QueryRow(ctx, "SELECT clock_timestamp()").Scan(&before))
	require.NoError(t, manager.WithinTransaction(ctx, func(ctx context.Context) error {
		cursor, dbNow, err := repository.LockMachineForTelemetry(ctx, event.MachineID)
		if err != nil {
			return err
		}
		assert.Nil(t, cursor)
		assert.False(t, dbNow.Before(before))
		return repository.UpdateMachineTelemetryCursor(ctx, event.MachineID, event.ObservedAt.Add(time.Nanosecond))
	}))
	err := manager.WithinTransaction(ctx, func(ctx context.Context) error {
		cursor, _, err := repository.LockMachineForTelemetry(ctx, event.MachineID)
		if err != nil {
			return err
		}
		assert.Equal(t, &event.ObservedAt, cursor)
		_, _, err = repository.LockMachineForTelemetry(ctx, uuid.NewString())
		return err
	})
	require.ErrorIs(t, err, machine.ErrNotFound)
	machineRepository := machinepostgres.New(pool)
	owned, err := machineRepository.FindByOwner(ctx, target.OwnerUserID, event.MachineID)
	require.NoError(t, err)
	assert.Equal(t, &event.ObservedAt, owned.LastTelemetryObservedAt)
	ownedMachines, err := machineRepository.ListByOwner(ctx, target.OwnerUserID)
	require.NoError(t, err)
	assert.Equal(t, []machine.Machine{owned}, ownedMachines)
}

func TestTelemetryMachineReceiptTimeIsSampledAfterLockWait(t *testing.T) {
	t.Parallel()
	pool := testutil.Postgres(t)
	target, _ := enrollmentMachines(t, pool)
	ctx, cancel := context.WithTimeout(t.Context(), 8*time.Second)
	defer cancel()
	blocker, err := pool.Begin(ctx)
	require.NoError(t, err)
	defer func() { _ = blocker.Rollback(t.Context()) }()

	var blockerPID int
	require.NoError(t, blocker.QueryRow(ctx, "SELECT pg_backend_pid() FROM machines WHERE id=$1 FOR UPDATE",
		target.ID).Scan(&blockerPID))
	repository := telemetrypostgres.New()
	manager := transactionpostgres.New(pool)
	type result struct {
		dbNow time.Time
		err   error
	}
	results := make(chan result, 1)
	go func() {
		var dbNow time.Time
		txErr := manager.WithinTransaction(ctx, func(txCtx context.Context) error {
			_, sampled, lockErr := repository.LockMachineForTelemetry(txCtx, target.ID)
			dbNow = sampled
			return lockErr
		})
		results <- result{dbNow: dbNow, err: txErr}
	}()
	require.Eventually(t, func() bool {
		return telemetryLockHasWaiter(ctx, pool, blockerPID)
	}, 5*time.Second, 10*time.Millisecond)

	var beforeRelease time.Time
	require.NoError(t, blocker.QueryRow(ctx, "SELECT clock_timestamp()").Scan(&beforeRelease))
	require.NoError(t, blocker.Commit(ctx))
	select {
	case actual := <-results:
		require.NoError(t, actual.err)
		assert.False(t, actual.dbNow.Before(beforeRelease))
	case <-ctx.Done():
		t.Fatal("telemetry waiter did not finish after the machine lock was released")
	}
}

func telemetryLockHasWaiter(ctx context.Context, pool *pgxpool.Pool, blockerPID int) bool {
	var blocked bool
	err := pool.QueryRow(ctx, `SELECT EXISTS (
		SELECT 1 FROM pg_stat_activity WHERE $1=ANY(pg_blocking_pids(pid))
	)`, blockerPID).Scan(&blocked)
	return err == nil && blocked
}

func TestTelemetryExpiredIdempotencyCleanup(t *testing.T) {
	t.Parallel()
	pool := testutil.Postgres(t)
	event, _ := telemetryStorageEvent(t, pool)
	active := event
	active.EventID = uuid.NewString()
	active.ObservedAt = event.ObservedAt.Add(time.Microsecond)
	expiry := time.Date(2026, time.December, 23, 12, 0, 0, 0, time.UTC)
	ctx := t.Context()
	repository := telemetrypostgres.New()
	manager := transactionpostgres.New(pool)
	require.NoError(t, manager.WithinTransaction(ctx, func(ctx context.Context) error {
		if err := storeTelemetryEvent(ctx, repository, event); err != nil {
			return err
		}
		return storeTelemetryEvent(ctx, repository, active)
	}))

	require.NoError(t, manager.WithinTransaction(ctx, func(ctx context.Context) error {
		if err := repository.DeleteExpiredTelemetryEvents(
			ctx,
			active.MachineID,
			active.DeviceID,
			active.EventID,
			expiry.Add(-time.Microsecond),
		); err != nil {
			return err
		}
		hash, exists, err := repository.FindTelemetryEvent(ctx, event.DeviceID, event.EventID)
		assert.NoError(t, err)
		assert.True(t, exists)
		assert.Equal(t, event.PayloadHash, hash)
		if err = repository.DeleteExpiredTelemetryEvents(
			ctx,
			active.MachineID,
			active.DeviceID,
			active.EventID,
			expiry,
		); err != nil {
			return err
		}
		_, exists, err = repository.FindTelemetryEvent(ctx, event.DeviceID, event.EventID)
		assert.NoError(t, err)
		assert.False(t, exists)
		_, exists, err = repository.FindTelemetryEvent(ctx, active.DeviceID, active.EventID)
		assert.True(t, exists)
		return err
	}))

	var present bool
	var cursor time.Time
	require.NoError(t, pool.QueryRow(ctx, `SELECT
		EXISTS (SELECT 1 FROM telemetry_outbox WHERE device_id=$1 AND event_id=$2 AND delivered_at IS NULL),
		(SELECT last_telemetry_observed_at FROM machines WHERE id=$3)`, event.DeviceID, event.EventID, event.MachineID).
		Scan(&present, &cursor))
	assert.True(t, present)
	assert.WithinDuration(t, active.ObservedAt, cursor, 0)
}

func storeTelemetryEvent(ctx context.Context, repository *telemetrypostgres.Repository, event telemetry.Event) error {
	if err := repository.InsertTelemetryEvent(ctx, event); err != nil {
		return err
	}
	if err := repository.UpdateMachineTelemetryCursor(ctx, event.MachineID, event.ObservedAt); err != nil {
		return err
	}
	return repository.InsertTelemetryOutbox(ctx, event)
}

func assertTelemetryIdentity(t *testing.T, pool *pgxpool.Pool, event telemetry.Event) {
	t.Helper()
	var machineID string
	var observedAt, expiresAt time.Time
	var payloadHash []byte
	require.NoError(t, pool.QueryRow(t.Context(), `SELECT machine_id::text,observed_at,payload_hash,expires_at
		FROM telemetry_events WHERE device_id=$1 AND event_id=$2`, event.DeviceID, event.EventID).
		Scan(&machineID, &observedAt, &payloadHash, &expiresAt))
	assert.Equal(t, event.MachineID, machineID)
	assert.Equal(t, event.PayloadHash[:], payloadHash)
	assert.WithinDuration(t, event.ObservedAt, observedAt, 0)
	assert.WithinDuration(t, time.Date(2026, time.December, 23, 12, 0, 0, 0, time.UTC), expiresAt, 0)
}

func telemetryStorageEvent(t *testing.T, pool *pgxpool.Pool) (telemetry.Event, machine.Machine) {
	t.Helper()
	target, _ := enrollmentMachines(t, pool)
	deviceID := createTelemetryStorageDevice(t, pool, target)
	frequency := 39.1
	return telemetry.Event{
		Observation: telemetry.Observation{
			SchemaVersion: 1, EventID: uuid.NewString(), ObservedAt: target.CreatedAt,
			OperatingState: "running", VibrationRMSG: 0.043, DominantFrequencyHz: &frequency,
		},
		DeviceID: deviceID, MachineID: target.ID, ReceivedAt: target.CreatedAt.Add(time.Second),
		PayloadHash: sha256.Sum256([]byte("telemetry fixture")),
	}, target
}

func createTelemetryStorageDevice(t *testing.T, pool *pgxpool.Pool, target machine.Machine) string {
	t.Helper()
	device, _, _ := enrollmentDevice(target.CreatedAt)
	require.NoError(t, devicepostgres.New(pool).Create(t.Context(), device))
	_, err := pool.Exec(t.Context(),
		`UPDATE devices SET machine_id=$1,claimed_at=$2,claim_verifier=NULL WHERE id=$3`,
		target.ID, target.CreatedAt, device.ID,
	)
	require.NoError(t, err)
	return device.ID
}
