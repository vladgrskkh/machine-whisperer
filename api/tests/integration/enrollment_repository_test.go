//go:build integration

package integration_test

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vladgrskkh/machine-whisperer/api/internal/domain/device"
	"github.com/vladgrskkh/machine-whisperer/api/internal/domain/machine"
	devicepostgres "github.com/vladgrskkh/machine-whisperer/api/internal/infrastructure/device/postgres"
	machinepostgres "github.com/vladgrskkh/machine-whisperer/api/internal/infrastructure/machine/postgres"
	transactionpostgres "github.com/vladgrskkh/machine-whisperer/api/internal/infrastructure/transaction/postgres"
	userpostgres "github.com/vladgrskkh/machine-whisperer/api/internal/infrastructure/user/postgres"
	"github.com/vladgrskkh/machine-whisperer/api/internal/testutil"
)

func TestMachineRepositoryOwnershipAndTransactions(t *testing.T) {
	t.Parallel()
	pool := testutil.Postgres(t)
	first, other := enrollmentMachines(t, pool)
	ctx := t.Context()
	repository := machinepostgres.New(pool)
	assert.ErrorIs(t, repository.Create(ctx, first), machine.ErrConflict)
	noOwner := machine.Machine{
		ID:          uuid.NewString(),
		OwnerUserID: uuid.NewString(),
		Name:        "No owner",
		CreatedAt:   first.CreatedAt,
	}
	assert.ErrorIs(t, repository.Create(ctx, noOwner), machine.ErrNotFound)
	view, err := repository.FindByOwner(ctx, first.OwnerUserID, first.ID)
	require.NoError(t, err)
	assert.Equal(t, first, view)
	_, err = repository.FindByOwner(ctx, other.OwnerUserID, first.ID)
	assert.ErrorIs(t, err, machine.ErrNotFound)

	newer := machine.Machine{
		ID:          uuid.NewString(),
		OwnerUserID: first.OwnerUserID,
		Name:        "Newer motor",
		CreatedAt:   first.CreatedAt.Add(time.Second),
	}
	expected := errors.New("roll back machine creation")
	err = transactionpostgres.New(pool).WithinTransaction(ctx, func(ctx context.Context) error {
		if createErr := repository.Create(ctx, newer); createErr != nil {
			return createErr
		}
		machines, listErr := repository.ListByOwner(ctx, first.OwnerUserID)
		assert.NoError(t, listErr)
		assert.Equal(t, []machine.Machine{newer, first}, machines)
		return expected
	})
	require.ErrorIs(t, err, expected)
	owned, err := repository.ListByOwner(ctx, first.OwnerUserID)
	require.NoError(t, err)
	assert.Equal(t, []machine.Machine{first}, owned)
	owned, err = repository.ListByOwner(ctx, uuid.NewString())
	require.NoError(t, err)
	assert.Equal(t, []machine.Machine{}, owned)
}

func TestDeviceRepositoryConsumesClaimAtomically(t *testing.T) {
	t.Parallel()
	pool := testutil.Postgres(t)
	owned, other := enrollmentMachines(t, pool)
	ctx := t.Context()
	repository := devicepostgres.New(pool)
	manager := transactionpostgres.New(pool)
	value, key, code := enrollmentDevice(owned.CreatedAt)
	require.NoError(t, repository.Create(ctx, value))
	assert.ErrorIs(t, repository.Create(ctx, value), device.ErrConflict)
	assert.ErrorIs(
		t,
		repository.Claim(ctx, value.ID, owned.ID, owned.OwnerUserID, value.CreatedAt),
		device.ErrTransactionRequired,
	)
	_, err := repository.FindForClaim(ctx, value.ID)
	assert.ErrorIs(t, err, device.ErrTransactionRequired)

	var storedKey, storedClaim []byte
	require.NoError(t, pool.QueryRow(ctx, `SELECT key_verifier,claim_verifier FROM devices WHERE id=$1`, value.ID).
		Scan(&storedKey, &storedClaim))
	assert.Equal(t, value.KeyVerifier[:], storedKey)
	assert.Equal(t, value.ClaimVerifier[:], storedClaim)
	assert.NotEqual(t, key[:], storedKey)
	assert.NotEqual(t, code[:], storedClaim)

	claimAt := value.CreatedAt.Add(time.Second)
	assert.ErrorIs(t, manager.WithinTransaction(ctx, func(ctx context.Context) error {
		return repository.Claim(ctx, value.ID, other.ID, owned.OwnerUserID, claimAt)
	}), device.ErrClaimUnavailable)
	assert.ErrorIs(t, manager.WithinTransaction(ctx, func(ctx context.Context) error {
		return repository.Claim(ctx, value.ID, owned.ID, owned.OwnerUserID, value.ClaimExpiresAt)
	}), device.ErrClaimUnavailable)

	expected := errors.New("roll back device claim")
	err = manager.WithinTransaction(ctx, func(ctx context.Context) error {
		locked, lookupErr := repository.FindForClaim(ctx, value.ID)
		if lookupErr != nil {
			return lookupErr
		}
		assert.Equal(t, value, locked)
		if claimErr := repository.Claim(ctx, value.ID, owned.ID, owned.OwnerUserID, claimAt); claimErr != nil {
			return claimErr
		}
		return expected
	})
	require.ErrorIs(t, err, expected)
	assertConcurrentDeviceClaims(t, pool, value, owned, claimAt)

	var machineID string
	var claimedAt time.Time
	require.NoError(
		t,
		pool.QueryRow(ctx, `SELECT machine_id::text,claimed_at,key_verifier,claim_verifier FROM devices WHERE id=$1`, value.ID).
			Scan(&machineID, &claimedAt, &storedKey, &storedClaim),
	)
	assert.Equal(t, owned.ID, machineID)
	assert.True(t, claimAt.Equal(claimedAt))
	assert.Equal(t, value.KeyVerifier[:], storedKey)
	assert.Nil(t, storedClaim)
}

func TestDeviceRepositoryRejectsRevokedAndMissingDevices(t *testing.T) {
	t.Parallel()
	pool := testutil.Postgres(t)
	owned, _ := enrollmentMachines(t, pool)
	value, _, _ := enrollmentDevice(owned.CreatedAt)
	ctx := t.Context()
	repository := devicepostgres.New(pool)
	manager := transactionpostgres.New(pool)
	require.NoError(t, repository.Create(ctx, value))
	_, err := pool.Exec(ctx, `UPDATE devices SET revoked_at=$2 WHERE id=$1`, value.ID, value.CreatedAt)
	require.NoError(t, err)
	for _, id := range []string{value.ID, uuid.NewString()} {
		assert.ErrorIs(t, manager.WithinTransaction(ctx, func(ctx context.Context) error {
			_, lookupErr := repository.FindForClaim(ctx, id)
			return lookupErr
		}), device.ErrClaimUnavailable)
		assert.ErrorIs(t, manager.WithinTransaction(ctx, func(ctx context.Context) error {
			return repository.Claim(ctx, id, owned.ID, owned.OwnerUserID, value.CreatedAt)
		}), device.ErrClaimUnavailable)
	}
}

func assertConcurrentDeviceClaims(
	t *testing.T,
	pool *pgxpool.Pool,
	value device.Device,
	owned machine.Machine,
	at time.Time,
) {
	t.Helper()
	const claimRaceTimeout = 10 * time.Second
	ctx, cancel := context.WithTimeout(t.Context(), claimRaceTimeout)
	defer cancel()
	repository := devicepostgres.New(pool)
	manager := transactionpostgres.New(pool)
	start, results := make(chan struct{}), make(chan error, 2)
	for range 2 {
		go func() {
			<-start
			results <- manager.WithinTransaction(ctx, func(ctx context.Context) error {
				if _, err := repository.FindForClaim(ctx, value.ID); err != nil {
					return err
				}
				return repository.Claim(ctx, value.ID, owned.ID, owned.OwnerUserID, at)
			})
		}()
	}
	close(start)
	var successes int
	for range 2 {
		if err := <-results; err == nil {
			successes++
		} else {
			assert.ErrorIs(t, err, device.ErrClaimUnavailable)
		}
	}
	assert.Equal(t, 1, successes)
}

func enrollmentMachines(t *testing.T, pool *pgxpool.Pool) (machine.Machine, machine.Machine) {
	t.Helper()
	ctx := t.Context()
	users := userpostgres.New(pool)
	firstOwner, err := users.Create(ctx, "first-owner@example.com", "test hash")
	require.NoError(t, err)
	otherOwner, err := users.Create(ctx, "other-owner@example.com", "test hash")
	require.NoError(t, err)
	now := time.Date(2026, time.November, 23, 12, 0, 0, 0, time.UTC)
	owned := machine.Machine{ID: uuid.NewString(), OwnerUserID: firstOwner.ID, Name: "First motor", CreatedAt: now}
	other := machine.Machine{ID: uuid.NewString(), OwnerUserID: otherOwner.ID, Name: "Other motor", CreatedAt: now}
	repository := machinepostgres.New(pool)
	require.NoError(t, repository.Create(ctx, owned))
	require.NoError(t, repository.Create(ctx, other))
	return owned, other
}

func enrollmentDevice(now time.Time) (device.Device, [32]byte, [32]byte) {
	var key, code [32]byte
	rand.Read(key[:])
	rand.Read(code[:])
	return device.Device{
		ID: uuid.NewString(), KeyVerifier: sha256.Sum256(key[:]), ClaimVerifier: sha256.Sum256(code[:]),
		ClaimExpiresAt: now.Add(time.Hour), CreatedAt: now,
	}, key, code
}
