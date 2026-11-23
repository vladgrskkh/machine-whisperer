package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgerrcode"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/vladgrskkh/machine-whisperer/api/internal/domain/device"
	transaction "github.com/vladgrskkh/machine-whisperer/api/internal/infrastructure/transaction/postgres"
)

type Repository struct{ pool *pgxpool.Pool }

func New(pool *pgxpool.Pool) *Repository { return &Repository{pool: pool} }

func (r *Repository) Create(ctx context.Context, value device.Device) error {
	exec := r.pool.Exec
	if tx := transaction.FromContext(ctx); tx != nil {
		exec = tx.Exec
	}
	_, err := exec(ctx, `INSERT INTO devices (id,key_verifier,claim_verifier,claim_expires_at,created_at)
		VALUES ($1,$2,$3,$4,$5)`, value.ID, value.KeyVerifier[:], value.ClaimVerifier[:], value.ClaimExpiresAt, value.CreatedAt)
	if pgErr, ok := errors.AsType[*pgconn.PgError](err); ok && pgErr.Code == pgerrcode.UniqueViolation {
		return device.ErrConflict
	}
	if err != nil {
		return fmt.Errorf("insert device: %w", err)
	}
	return nil
}

// FindForClaim locks the verifier until its use case commits or rolls back the claim.
func (r *Repository) FindForClaim(ctx context.Context, id string) (device.Device, error) {
	tx := transaction.FromContext(ctx)
	if tx == nil {
		return device.Device{}, device.ErrTransactionRequired
	}
	var value device.Device
	var keyVerifier, claimVerifier []byte
	err := tx.QueryRow(ctx, `SELECT id::text,key_verifier,claim_verifier,claim_expires_at,created_at
		FROM devices WHERE id=$1 AND claim_verifier IS NOT NULL AND revoked_at IS NULL FOR UPDATE`, id).
		Scan(&value.ID, &keyVerifier, &claimVerifier, &value.ClaimExpiresAt, &value.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return device.Device{}, device.ErrClaimUnavailable
	}
	if err != nil {
		return device.Device{}, fmt.Errorf("lock device for claim: %w", err)
	}
	copy(value.KeyVerifier[:], keyVerifier)
	copy(value.ClaimVerifier[:], claimVerifier)
	value.ClaimExpiresAt = value.ClaimExpiresAt.UTC()
	value.CreatedAt = value.CreatedAt.UTC()
	return value, nil
}

func (r *Repository) LockMachineOwner(ctx context.Context, machineID, ownerUserID string) error {
	tx := transaction.FromContext(ctx)
	if tx == nil {
		return device.ErrTransactionRequired
	}
	var id string
	err := tx.QueryRow(ctx, `SELECT id::text FROM machines WHERE id=$1 AND owner_user_id=$2 FOR SHARE`,
		machineID, ownerUserID).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return device.ErrClaimUnavailable
	}
	if err != nil {
		return fmt.Errorf("lock machine for claim: %w", err)
	}
	return nil
}

func (r *Repository) Claim(ctx context.Context, deviceID, machineID, ownerUserID string, at time.Time) error {
	tx := transaction.FromContext(ctx)
	if tx == nil {
		return device.ErrTransactionRequired
	}
	// Hold ownership stable until the device binding commits.
	tag, err := tx.Exec(ctx, `UPDATE devices
		SET machine_id=target.id,claimed_at=$4,claim_verifier=NULL
		FROM (SELECT id FROM machines WHERE id=$2 AND owner_user_id=$3 FOR SHARE) AS target
		WHERE devices.id=$1 AND devices.machine_id IS NULL AND devices.claim_verifier IS NOT NULL
		AND devices.revoked_at IS NULL AND devices.claim_expires_at > $4`, deviceID, machineID, ownerUserID, at)
	if err != nil {
		return fmt.Errorf("claim device: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return device.ErrClaimUnavailable
	}
	return nil
}
