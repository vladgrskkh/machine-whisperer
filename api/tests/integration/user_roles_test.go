//go:build integration

package integration_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgerrcode"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vladgrskkh/machine-whisperer/api/internal/domain/user"
	transactionpostgres "github.com/vladgrskkh/machine-whisperer/api/internal/infrastructure/transaction/postgres"
	userpostgres "github.com/vladgrskkh/machine-whisperer/api/internal/infrastructure/user/postgres"
)

const provisionerRole = "device_provisioner"
const insertRole = "INSERT INTO user_roles (user_id,role) VALUES ($1,$2)"

func TestUserRoleStorageConstraints(t *testing.T) {
	t.Parallel()
	fixture := loginFixture(t)
	ctx := t.Context()
	account, err := fixture.signup.Signup(ctx, "role-constraints@example.com", exactLoginPassword)
	require.NoError(t, err)
	var count int
	require.NoError(
		t,
		fixture.pool.QueryRow(ctx, "SELECT count(*) FROM user_roles WHERE user_id=$1", account.ID).Scan(&count),
	)
	assert.Zero(t, count)

	_, err = fixture.pool.Exec(ctx, insertRole, account.ID, "unsupported_role")
	requirePGCode(t, err, pgerrcode.CheckViolation)
	_, err = fixture.pool.Exec(ctx, insertRole, uuid.NewString(), provisionerRole)
	requirePGCode(t, err, pgerrcode.ForeignKeyViolation)
	_, err = fixture.pool.Exec(ctx, insertRole, account.ID, provisionerRole)
	require.NoError(t, err)
	_, err = fixture.pool.Exec(ctx, insertRole, account.ID, provisionerRole)
	requirePGCode(t, err, pgerrcode.UniqueViolation)
	_, err = fixture.pool.Exec(ctx, "DELETE FROM users WHERE id=$1", account.ID)
	require.NoError(t, err)
	require.NoError(
		t,
		fixture.pool.QueryRow(ctx, "SELECT count(*) FROM user_roles WHERE user_id=$1", account.ID).Scan(&count),
	)
	assert.Zero(t, count)
}

func TestLoginAndRefreshRoles(t *testing.T) {
	t.Parallel()
	fixture := loginFixture(t)
	ctx := t.Context()
	account, err := fixture.signup.Signup(ctx, refreshTestEmail, exactLoginPassword)
	require.NoError(t, err)
	ordinary := nextLoginSession(t, fixture)
	assert.Equal(t, []string{}, verifyRefreshAccess(t, fixture, ordinary).Roles)

	_, err = fixture.pool.Exec(ctx, insertRole, account.ID, provisionerRole)
	require.NoError(t, err)
	privileged := nextLoginSession(t, fixture)
	assert.Equal(t, []string{provisionerRole}, verifyRefreshAccess(t, fixture, privileged).Roles)
	now := func() time.Time { return fixture.now.Add(time.Second) }
	refresh := newRefresh(fixture, now)
	granted, err := refresh.Refresh(ctx, ordinary.RefreshToken)
	require.NoError(t, err)
	assert.Equal(t, []string{provisionerRole}, verifyRefreshAccess(t, fixture, granted).Roles)

	_, err = fixture.pool.Exec(ctx, "DELETE FROM user_roles WHERE user_id=$1 AND role=$2", account.ID, provisionerRole)
	require.NoError(t, err)
	removed, err := refresh.Refresh(ctx, granted.RefreshToken)
	require.NoError(t, err)
	assert.Equal(t, []string{}, verifyRefreshAccess(t, fixture, removed).Roles)
	assert.Equal(t, []string{provisionerRole}, verifyRefreshAccess(t, fixture, granted).Roles)
}

func TestRefreshReadsRolesAfterConsumption(t *testing.T) {
	t.Parallel()
	fixture := loginFixture(t)
	ctx := t.Context()
	account, err := fixture.signup.Signup(ctx, refreshTestEmail, exactLoginPassword)
	require.NoError(t, err)
	_, err = fixture.pool.Exec(ctx, insertRole, account.ID, provisionerRole)
	require.NoError(t, err)
	original := nextLoginSession(t, fixture)
	assert.Equal(t, []string{provisionerRole}, verifyRefreshAccess(t, fixture, original).Roles)
	_, err = fixture.pool.Exec(ctx, `
		CREATE FUNCTION remove_role_on_consume() RETURNS trigger LANGUAGE plpgsql AS $$
		BEGIN DELETE FROM user_roles WHERE user_id = (SELECT user_id FROM sessions WHERE id = NEW.session_id);
		RETURN NEW; END $$;
		CREATE TRIGGER remove_role_on_consume AFTER UPDATE OF consumed_at ON refresh_tokens
		FOR EACH ROW EXECUTE FUNCTION remove_role_on_consume()`)
	require.NoError(t, err)
	refresh := newRefresh(fixture, func() time.Time { return fixture.now.Add(time.Second) })
	next, err := refresh.Refresh(ctx, original.RefreshToken)
	require.NoError(t, err)
	assert.Equal(t, []string{}, verifyRefreshAccess(t, fixture, next).Roles)
}

func TestUserRoleRepositoryTransactions(t *testing.T) {
	t.Parallel()
	pool := accountPostgres.Pool(t)
	repository := userpostgres.New(pool)
	ctx := t.Context()
	account, err := repository.Create(ctx, "role-rollback@example.com", "test hash")
	require.NoError(t, err)
	wantRole := []string{provisionerRole}
	expected := errors.New("roll back role change")
	err = transactionpostgres.New(pool).WithinTransaction(ctx, func(ctx context.Context) error {
		if grantErr := repository.GrantRole(ctx, account.ID, provisionerRole); grantErr != nil {
			return grantErr
		}
		roles, listErr := repository.ListRoles(ctx, account.ID)
		assert.NoError(t, listErr)
		assert.Equal(t, wantRole, roles)
		return expected
	})
	require.ErrorIs(t, err, expected)
	roles, err := repository.ListRoles(ctx, account.ID)
	require.NoError(t, err)
	assert.Equal(t, []string{}, roles)

	assert.ErrorIs(t, repository.GrantRole(ctx, uuid.NewString(), provisionerRole), user.ErrNotFound)
	require.NoError(t, repository.GrantRole(ctx, account.ID, provisionerRole))
	require.NoError(t, repository.GrantRole(ctx, account.ID, provisionerRole))
	roles, err = repository.ListRoles(ctx, account.ID)
	require.NoError(t, err)
	assert.Equal(t, wantRole, roles)
	require.NoError(t, repository.RevokeRole(ctx, account.ID, provisionerRole))
	require.NoError(t, repository.RevokeRole(ctx, account.ID, provisionerRole))
	roles, err = repository.ListRoles(ctx, account.ID)
	require.NoError(t, err)
	assert.Equal(t, []string{}, roles)
}
