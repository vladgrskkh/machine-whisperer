//go:build integration

package integration_test

import (
	"crypto/sha256"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgerrcode"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vladgrskkh/machine-whisperer/api/internal/testutil"
)

func TestEnrollmentSchemaConstraints(t *testing.T) {
	t.Parallel()
	pool := testutil.Postgres(t)
	ctx := t.Context()
	ownerID, machineID, deviceID := uuid.NewString(), uuid.NewString(), uuid.NewString()
	now := time.Date(2026, time.November, 23, 12, 0, 0, 0, time.UTC)
	_, err := pool.Exec(
		ctx,
		"INSERT INTO users (id,email,password_hash) VALUES ($1,'enrollment@example.com','test hash')",
		ownerID,
	)
	require.NoError(t, err)

	const insertMachine = "INSERT INTO machines (id,owner_user_id,name,created_at) VALUES ($1,$2,'Test motor',$3)"
	_, err = pool.Exec(ctx, insertMachine, machineID, uuid.NewString(), now)
	requirePGCode(t, err, pgerrcode.ForeignKeyViolation)
	_, err = pool.Exec(ctx, insertMachine, "invalid UUID", ownerID, now)
	requirePGCode(t, err, pgerrcode.InvalidTextRepresentation)
	_, err = pool.Exec(ctx, insertMachine, machineID, ownerID, now)
	require.NoError(t, err)

	const insertDevice = `INSERT INTO devices
		(id,key_verifier,claim_verifier,created_at,claim_expires_at) VALUES ($1,$2,$3,$4,$5)`
	key := sha256.Sum256([]byte("fixture API secret"))
	code := sha256.Sum256([]byte("fixture claim code"))
	expires := now.Add(time.Hour)
	_, err = pool.Exec(ctx, insertDevice, deviceID, key[:31], code[:], now, expires)
	requirePGCode(t, err, pgerrcode.CheckViolation)
	_, err = pool.Exec(ctx, insertDevice, deviceID, key[:], code[:31], now, expires)
	requirePGCode(t, err, pgerrcode.CheckViolation)
	_, err = pool.Exec(ctx, insertDevice, deviceID, key[:], code[:], now, expires)
	require.NoError(t, err)
	otherCode := sha256.Sum256([]byte("different claim code"))
	_, err = pool.Exec(ctx, insertDevice, uuid.NewString(), key[:], otherCode[:], now, expires)
	requirePGCode(t, err, pgerrcode.UniqueViolation)
	otherKey := sha256.Sum256([]byte("different API secret"))
	_, err = pool.Exec(ctx, insertDevice, uuid.NewString(), otherKey[:], code[:], now, expires)
	requirePGCode(t, err, pgerrcode.UniqueViolation)
	_, err = pool.Exec(ctx, "UPDATE devices SET claim_verifier=NULL WHERE id=$1", deviceID)
	requirePGCode(t, err, pgerrcode.CheckViolation)
	const bindDevice = "UPDATE devices SET machine_id=$1, claimed_at=$2, claim_verifier=NULL WHERE id=$3"
	_, err = pool.Exec(ctx, bindDevice, uuid.NewString(), now, deviceID)
	requirePGCode(t, err, pgerrcode.ForeignKeyViolation)
	_, err = pool.Exec(ctx, bindDevice, machineID, now, deviceID)
	require.NoError(t, err)
	_, err = pool.Exec(ctx, "DELETE FROM users WHERE id=$1", ownerID)
	require.NoError(t, err)
	var machineExists, deviceExists bool
	require.NoError(t, pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM machines WHERE id=$1),
		EXISTS (SELECT 1 FROM devices WHERE id=$2)`, machineID, deviceID).Scan(&machineExists, &deviceExists))
	assert.False(t, machineExists)
	assert.False(t, deviceExists)
}

func TestEnrollmentMigrationsCanBeReapplied(t *testing.T) {
	t.Parallel()
	pool := testutil.Postgres(t)
	ctx := t.Context()
	provider := testutil.PostgresMigrations(t, pool)
	_, err := provider.Down(ctx)
	require.NoError(t, err)
	_, err = provider.Down(ctx)
	require.NoError(t, err)

	const tables = `SELECT to_regclass('users') IS NOT NULL,to_regclass('sessions') IS NOT NULL,
		to_regclass('user_roles') IS NOT NULL,to_regclass('machines') IS NOT NULL,to_regclass('devices') IS NOT NULL`
	var users, sessions, roles, machines, devices bool
	require.NoError(t, pool.QueryRow(ctx, tables).Scan(&users, &sessions, &roles, &machines, &devices))
	assert.True(t, users)
	assert.True(t, sessions)
	assert.False(t, roles)
	assert.False(t, machines)
	assert.False(t, devices)

	_, err = provider.Up(ctx)
	require.NoError(t, err)
	require.NoError(t, pool.QueryRow(ctx, tables).Scan(&users, &sessions, &roles, &machines, &devices))
	assert.True(t, roles)
	assert.True(t, machines)
	assert.True(t, devices)
}
