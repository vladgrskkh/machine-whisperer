//go:build integration

package enrollment_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"testing/iotest"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/trace/noop"

	"github.com/vladgrskkh/machine-whisperer/api/internal/application"
	"github.com/vladgrskkh/machine-whisperer/api/internal/domain/session"
	"github.com/vladgrskkh/machine-whisperer/api/internal/domain/user"
	"github.com/vladgrskkh/machine-whisperer/api/internal/handler/devices"
	"github.com/vladgrskkh/machine-whisperer/api/internal/handler/middleware"
	"github.com/vladgrskkh/machine-whisperer/api/internal/infrastructure/auth/token"
	"github.com/vladgrskkh/machine-whisperer/api/internal/infrastructure/device/credential"
	devicepostgres "github.com/vladgrskkh/machine-whisperer/api/internal/infrastructure/device/postgres"
	userpostgres "github.com/vladgrskkh/machine-whisperer/api/internal/infrastructure/user/postgres"
	"github.com/vladgrskkh/machine-whisperer/api/internal/testutil"
	deviceusecase "github.com/vladgrskkh/machine-whisperer/api/internal/usecase/device"
	"github.com/vladgrskkh/machine-whisperer/api/tests/integration/internal/testfixture"
)

const claimError = `{"error":{"code":"claim_unavailable","message":"Device claim unavailable"}}`

func TestProvisioningRequiresSignedRoleAndStoresOnlyVerifiers(t *testing.T) {
	t.Parallel()
	pool := testutil.Postgres(t)
	server, _ := testfixture.NewSessionServer(t, pool)
	id, ordinary := testfixture.SignupEnrollmentUser(t, server, testfixture.SessionSignupBody)

	for _, auth := range []http.Header{nil, testfixture.Bearer("invalid-jwt")} {
		response := testfixture.AuthHTTPRequest(t, server, http.MethodPost, testfixture.ProvisionPath, "", auth)
		assert.Equal(t, http.StatusUnauthorized, response.Status)
		assert.Equal(t, "no-store", response.Header.Get("Cache-Control"))
		assert.Equal(t, "Bearer", response.Header.Get("WWW-Authenticate"))
		assert.JSONEq(t, testfixture.AuthFailedJSON, response.Body)
	}
	assertProvisioningForbidden(t, server, ordinary.AccessToken)
	assertDeviceCount(t, pool, 0)

	roles := userpostgres.New(pool)
	require.NoError(t, roles.GrantRole(t.Context(), id, user.RoleDeviceProvisioner))
	assertProvisioningForbidden(t, server, ordinary.AccessToken)
	allowed := testfixture.LoginHTTPSession(t, server)
	first := testfixture.ProvisionEnrollment(t, server, allowed.AccessToken)
	second := testfixture.ProvisionEnrollment(t, server, allowed.AccessToken)
	assert.NotEqual(t, first.DeviceID, second.DeviceID)
	firstKey := testfixture.EnrollmentKey(t, first)
	secondKey := testfixture.EnrollmentKey(t, second)
	assert.NotEqual(t, firstKey, secondKey)
	assert.NotEqual(t, first.ClaimPayload.ClaimCode, second.ClaimPayload.ClaimCode)
	assert.NotEqual(t, firstKey, first.ClaimPayload.ClaimCode)
	assert.NotEqual(t, secondKey, second.ClaimPayload.ClaimCode)
	assertStoredEnrollment(t, pool, first, firstKey)
	assertStoredEnrollment(t, pool, second, secondKey)
	assertDeviceCount(t, pool, 2)
}

func TestProvisioningStoresAuthenticatedSubject(t *testing.T) {
	t.Parallel()
	pool := testutil.Postgres(t)
	server, _ := testfixture.NewSessionServer(t, pool)
	provisionerID, ordinary := testfixture.SignupEnrollmentUser(t, server, testfixture.SessionSignupBody)
	require.NoError(t, userpostgres.New(pool).GrantRole(t.Context(), provisionerID, user.RoleDeviceProvisioner))
	factory := testfixture.ReadHTTPTokenPair(
		t,
		testfixture.PostRefreshToken(t, server, testfixture.RefreshPath, ordinary.RefreshToken),
	)
	enrolled := testfixture.ProvisionEnrollment(t, server, factory.AccessToken)

	var storedProvisioner *string
	const query = "SELECT provisioned_by_user_id::text FROM devices WHERE id=$1"
	require.NoError(t, pool.QueryRow(t.Context(), query, enrolled.DeviceID).Scan(&storedProvisioner))
	require.NotNil(t, storedProvisioner)
	assert.Equal(t, provisionerID, *storedProvisioner)

	_, err := pool.Exec(t.Context(), "DELETE FROM users WHERE id=$1", provisionerID)
	require.NoError(t, err)
	require.NoError(t, pool.QueryRow(t.Context(), query, enrolled.DeviceID).Scan(&storedProvisioner))
	assert.Nil(t, storedProvisioner)
}

func TestDemoCleanupSerializesLateProvisioning(t *testing.T) {
	t.Parallel()
	pool := testutil.Postgres(t)
	server, _ := testfixture.NewSessionServer(t, pool)
	demoID, ordinary := testfixture.SignupEnrollmentUser(t, server, testfixture.SessionSignupBody)
	otherID, otherOrdinary := testfixture.SignupEnrollmentUser(
		t,
		server,
		`{"email":"other-provisioner@example.com","password":"long enough password"}`,
	)
	roles := userpostgres.New(pool)
	require.NoError(t, roles.GrantRole(t.Context(), demoID, user.RoleDeviceProvisioner))
	require.NoError(t, roles.GrantRole(t.Context(), otherID, user.RoleDeviceProvisioner))
	demo := testfixture.ReadHTTPTokenPair(
		t,
		testfixture.PostRefreshToken(t, server, testfixture.RefreshPath, ordinary.RefreshToken),
	)
	other := testfixture.ReadHTTPTokenPair(
		t,
		testfixture.PostRefreshToken(t, server, testfixture.RefreshPath, otherOrdinary.RefreshToken),
	)

	testfixture.ProvisionEnrollment(t, server, demo.AccessToken)
	otherDevice := testfixture.ProvisionEnrollment(t, server, other.AccessToken)

	ctx := t.Context()
	tx, err := pool.Begin(ctx)
	require.NoError(t, err)
	defer tx.Rollback(context.WithoutCancel(ctx))
	_, err = tx.Exec(ctx, "SELECT id FROM users WHERE id=$1 FOR UPDATE", demoID)
	require.NoError(t, err)

	type result struct {
		response testfixture.AuthHTTPResponse
		err      error
	}
	completed := make(chan result, 1)
	go func() {
		response, requestErr := testfixture.SendAuthHTTPRequest(
			ctx,
			server.Client(),
			http.MethodPost,
			server.URL+testfixture.ProvisionPath,
			"",
			testfixture.Bearer(demo.AccessToken),
		)
		completed <- result{response: response, err: requestErr}
	}()
	require.EventuallyWithT(t, func(collect *assert.CollectT) {
		var blocked bool
		queryErr := pool.QueryRow(ctx, `SELECT EXISTS (
			SELECT 1 FROM pg_stat_activity WHERE $1::int = ANY(pg_blocking_pids(pid))
		)`, int64(tx.Conn().PgConn().PID())).Scan(&blocked)
		require.NoError(collect, queryErr)
		assert.True(collect, blocked)
	}, 5*time.Second, 10*time.Millisecond)

	tag, err := tx.Exec(ctx, "DELETE FROM devices WHERE machine_id IS NULL AND provisioned_by_user_id=$1", demoID)
	require.NoError(t, err)
	assert.EqualValues(t, 1, tag.RowsAffected())
	_, err = tx.Exec(ctx, "DELETE FROM users WHERE id=$1", demoID)
	require.NoError(t, err)
	require.NoError(t, tx.Commit(ctx))

	attempt := <-completed
	require.NoError(t, attempt.err)
	assert.Equal(t, http.StatusInternalServerError, attempt.response.Status)
	assert.JSONEq(t, testfixture.InternalAuthErrorJSON, attempt.response.Body)
	assertDeviceCount(t, pool, 1)

	var ownerID string
	require.NoError(
		t,
		pool.QueryRow(ctx, "SELECT provisioned_by_user_id::text FROM devices WHERE id=$1", otherDevice.DeviceID).
			Scan(&ownerID),
	)
	assert.Equal(t, otherID, ownerID)
}

func TestProvisioningRoleRevocationFollowsAccessTokenExpiry(t *testing.T) {
	t.Parallel()
	pool := testutil.Postgres(t)
	server, setElapsed := testfixture.NewSessionServer(t, pool)
	id, ordinary := testfixture.SignupEnrollmentUser(t, server, testfixture.SessionSignupBody)
	roles := userpostgres.New(pool)
	require.NoError(t, roles.GrantRole(t.Context(), id, user.RoleDeviceProvisioner))
	allowed := testfixture.ReadHTTPTokenPair(
		t,
		testfixture.PostRefreshToken(t, server, testfixture.RefreshPath, ordinary.RefreshToken),
	)
	testfixture.ProvisionEnrollment(t, server, allowed.AccessToken)
	require.NoError(t, roles.RevokeRole(t.Context(), id, user.RoleDeviceProvisioner))
	refreshed := testfixture.ReadHTTPTokenPair(
		t,
		testfixture.PostRefreshToken(t, server, testfixture.RefreshPath, allowed.RefreshToken),
	)
	assertProvisioningForbidden(t, server, refreshed.AccessToken)
	setElapsed(15*time.Minute + 29*time.Second)
	testfixture.ProvisionEnrollment(t, server, allowed.AccessToken)
	setElapsed(15*time.Minute + 31*time.Second)
	response := testfixture.AuthHTTPRequest(
		t,
		server,
		http.MethodPost,
		testfixture.ProvisionPath,
		"",
		testfixture.Bearer(allowed.AccessToken),
	)
	assert.Equal(t, http.StatusUnauthorized, response.Status)
	assert.Equal(t, "no-store", response.Header.Get("Cache-Control"))
	assert.JSONEq(t, testfixture.AuthFailedJSON, response.Body)
	assertDeviceCount(t, pool, 2)
}

func TestProvisioningDatabaseFailureIsSanitized(t *testing.T) {
	t.Parallel()
	pool := testutil.Postgres(t)
	server, _ := testfixture.NewSessionServer(t, pool)
	factory := testfixture.StartFactorySession(t, pool, server)
	_, err := pool.Exec(t.Context(), `ALTER TABLE devices ADD CONSTRAINT internal_enrollment_failure CHECK (false)`)
	require.NoError(t, err)
	response := testfixture.AuthHTTPRequest(
		t,
		server,
		http.MethodPost,
		testfixture.ProvisionPath,
		"",
		testfixture.Bearer(factory.AccessToken),
	)
	assert.Equal(t, http.StatusInternalServerError, response.Status)
	assert.Equal(t, "no-store", response.Header.Get("Cache-Control"))
	assert.JSONEq(t, testfixture.InternalAuthErrorJSON, response.Body)
	assertDeviceCount(t, pool, 0)
}

func TestProvisioningEntropyFailureWritesNoDeviceOrSecret(t *testing.T) {
	t.Parallel()
	pool := testutil.Postgres(t)
	apiSecret := bytes.Repeat([]byte{42}, sha256.Size)
	generatorFailure := errors.New("private entropy error")
	usecase := deviceusecase.NewProvision(
		devicepostgres.New(pool),
		token.IDs{},
		credential.NewGenerator(bytes.NewReader(apiSecret)),
		credential.NewGenerator(iotest.ErrReader(generatorFailure)),
		time.Hour,
		noop.NewTracerProvider().Tracer("test"),
	)
	keyID, signing, verifying, _ := testutil.AuthKeys(t)
	keys, err := token.LoadKeys(keyID, signing, verifying)
	require.NoError(t, err)
	now := time.Now().UTC().Truncate(time.Second)
	jwt, err := token.NewJWT(keys, func() time.Time { return now })
	require.NoError(t, err)
	access, err := jwt.Sign(t.Context(), session.AccessClaims{
		UserID: uuid.NewString(), SessionID: uuid.NewString(), JTI: uuid.NewString(),
		IssuedAt: now, ExpiresAt: now.Add(15 * time.Minute), Roles: []string{user.RoleDeviceProvisioner},
	})
	require.NoError(t, err)

	var logs bytes.Buffer
	handler := middleware.AccessToken(jwt)(devices.NewProvision(usecase))
	server := httptest.NewUnstartedServer(middleware.NoStore(handler))
	server.Config.ErrorLog = slog.NewLogLogger(slog.NewTextHandler(&logs, nil), slog.LevelError)
	server.Start()
	t.Cleanup(server.Close)

	response := testfixture.AuthHTTPRequest(
		t,
		server,
		http.MethodPost,
		testfixture.ProvisionPath,
		"",
		testfixture.Bearer(access),
	)
	server.Close()
	assert.Equal(t, http.StatusInternalServerError, response.Status)
	assert.Equal(t, "no-store", response.Header.Get("Cache-Control"))
	assert.JSONEq(t, testfixture.InternalAuthErrorJSON, response.Body)
	assert.Empty(t, logs.String())
	assertDeviceCount(t, pool, 0)
}

func TestMachineHTTPOwnershipAndValidation(t *testing.T) {
	t.Parallel()
	pool := testutil.Postgres(t)
	server, _ := testfixture.NewSessionServer(t, pool)
	_, owner := testfixture.SignupEnrollmentUser(t, server, testfixture.SessionSignupBody)
	_, other := testfixture.SignupEnrollmentUser(
		t,
		server,
		`{"email":"other@example.com","password":"long enough password"}`,
	)

	response := testfixture.AuthHTTPRequest(t, server, http.MethodGet, testfixture.MachinesPath, "", nil)
	assert.Equal(t, http.StatusUnauthorized, response.Status)
	response = testfixture.AuthHTTPRequest(
		t,
		server,
		http.MethodGet,
		testfixture.MachinesPath,
		"",
		testfixture.Bearer(other.AccessToken),
	)
	assert.Equal(t, http.StatusOK, response.Status)
	assert.JSONEq(t, "[]", response.Body)

	created := testfixture.AuthHTTPRequest(
		t,
		server,
		http.MethodPost,
		testfixture.MachinesPath,
		`{"name":"  Pump \u00e9  "}`,
		testfixture.Bearer(owner.AccessToken),
	)
	machineID := testfixture.ReadEnrollmentMachine(t, created, "Pump \u00e9")
	response = testfixture.AuthHTTPRequest(
		t,
		server,
		http.MethodGet,
		testfixture.MachinesPath+"/"+machineID,
		"",
		testfixture.Bearer(owner.AccessToken),
	)
	assert.Equal(t, http.StatusOK, response.Status)
	assert.JSONEq(t, created.Body, response.Body)
	response = testfixture.AuthHTTPRequest(
		t,
		server,
		http.MethodGet,
		testfixture.MachinesPath,
		"",
		testfixture.Bearer(owner.AccessToken),
	)
	assert.Equal(t, http.StatusOK, response.Status)
	assert.JSONEq(t, "["+created.Body+"]", response.Body)

	for _, id := range []string{machineID, uuid.NewString()} {
		response = testfixture.AuthHTTPRequest(
			t,
			server,
			http.MethodGet,
			testfixture.MachinesPath+"/"+id,
			"",
			testfixture.Bearer(other.AccessToken),
		)
		assert.Equal(t, http.StatusNotFound, response.Status)
		assert.JSONEq(t, `{"error":{"code":"machine_not_found","message":"Machine not found"}}`, response.Body)
	}
	response = testfixture.AuthHTTPRequest(
		t,
		server,
		http.MethodGet,
		testfixture.MachinesPath,
		"",
		testfixture.Bearer(other.AccessToken),
	)
	assert.Equal(t, http.StatusOK, response.Status)
	assert.JSONEq(t, "[]", response.Body)

	for _, body := range []string{
		`{"name":null}`, `{"name":" "}`, `{"name":"motor\u0000"}`, `{"name":"motor\ud800"}`,
		`{"name":"motor","owner_user_id":"someone else"}`, `{"name":"one","name":"two"}`,
		`{"Name":"motor"}`, `{"name":"` + strings.Repeat("x", 101) + `"}`,
	} {
		response = testfixture.AuthHTTPRequest(
			t,
			server,
			http.MethodPost,
			testfixture.MachinesPath,
			body,
			testfixture.Bearer(owner.AccessToken),
		)
		assert.Equal(t, http.StatusBadRequest, response.Status)
	}
	response = testfixture.AuthHTTPRequest(
		t,
		server,
		http.MethodGet,
		testfixture.MachinesPath+"/invalid-id",
		"",
		testfixture.Bearer(owner.AccessToken),
	)
	assert.Equal(t, http.StatusBadRequest, response.Status)
}

func TestClaimHTTPIsAtomicAndUniform(t *testing.T) {
	t.Parallel()
	pool := testutil.Postgres(t)
	server, setElapsed := testfixture.NewSessionServer(t, pool)
	factory := testfixture.StartFactorySession(t, pool, server)
	_, owner := testfixture.SignupEnrollmentUser(
		t,
		server,
		`{"email":"owner@example.com","password":"long enough password"}`,
	)
	device := testfixture.ProvisionEnrollment(t, server, factory.AccessToken)
	expiring := testfixture.ProvisionEnrollment(t, server, factory.AccessToken)
	revoked := testfixture.ProvisionEnrollment(t, server, factory.AccessToken)
	foreign := testfixture.CreateEnrollmentMachine(t, server, factory.AccessToken)
	mine := testfixture.CreateEnrollmentMachine(t, server, owner.AccessToken)
	_, err := pool.Exec(t.Context(), "UPDATE devices SET revoked_at=created_at WHERE id=$1", revoked.DeviceID)
	require.NoError(t, err)

	response := testfixture.AuthHTTPRequest(
		t,
		server,
		http.MethodPost,
		testfixture.ClaimPath,
		testfixture.ClaimBody(device, mine),
		testfixture.Bearer(device.APIKey),
	)
	assert.Equal(t, http.StatusUnauthorized, response.Status)
	response = testfixture.AuthHTTPRequest(
		t,
		server,
		http.MethodGet,
		testfixture.MachinesPath,
		"",
		testfixture.Bearer(device.APIKey),
	)
	assert.Equal(t, http.StatusUnauthorized, response.Status)

	for _, body := range []string{
		testfixture.ClaimBody(device, foreign),
		testfixture.ClaimBody(device, uuid.NewString()),
		testfixture.ClaimBody(revoked, mine),
		strings.Replace(testfixture.ClaimBody(device, mine), device.DeviceID, uuid.NewString(), 1),
		strings.Replace(testfixture.ClaimBody(device, mine), device.ClaimPayload.ClaimCode, testfixture.UnknownRefreshToken, 1),
	} {
		assertClaimUnavailable(t, server, owner.AccessToken, body)
	}
	for _, body := range []string{
		`{"device_id":"invalid","machine_id":"` + mine + `","claim_code":"` + testfixture.UnknownRefreshToken + `"}`,
		strings.Replace(testfixture.ClaimBody(device, mine), device.ClaimPayload.ClaimCode, device.APIKey, 1),
		testfixture.ClaimBody(device, mine) + " {}",
		testfixture.ClaimBody(device, mine) + strings.Repeat(" ", 8192),
		`{"claim_code":"duplicate",` + testfixture.ClaimBody(device, mine)[1:],
	} {
		response = testfixture.AuthHTTPRequest(
			t,
			server,
			http.MethodPost,
			testfixture.ClaimPath,
			body,
			testfixture.Bearer(owner.AccessToken),
		)
		assert.Equal(t, http.StatusBadRequest, response.Status)
		assert.NotContains(t, response.Body, device.ClaimPayload.ClaimCode)
		assert.NotContains(t, response.Body, device.APIKey)
	}
	_, err = pool.Exec(t.Context(), "ALTER TABLE devices ADD CONSTRAINT refuse_claim CHECK (claimed_at IS NULL)")
	require.NoError(t, err)
	response = testfixture.AuthHTTPRequest(
		t,
		server,
		http.MethodPost,
		testfixture.ClaimPath,
		testfixture.ClaimBody(device, mine),
		testfixture.Bearer(owner.AccessToken),
	)
	assert.Equal(t, http.StatusInternalServerError, response.Status)
	assert.JSONEq(t, testfixture.InternalAuthErrorJSON, response.Body)
	_, err = pool.Exec(t.Context(), "ALTER TABLE devices DROP CONSTRAINT refuse_claim")
	require.NoError(t, err)
	response = testfixture.AuthHTTPRequest(
		t,
		server,
		http.MethodPost,
		testfixture.ClaimPath,
		testfixture.ClaimBody(device, mine),
		testfixture.Bearer(owner.AccessToken),
	)
	require.Equal(t, http.StatusOK, response.Status)
	assert.Equal(t, "no-store", response.Header.Get("Cache-Control"))
	assertClaimResponse(t, response.Body, device.DeviceID, mine)
	assertClaimUnavailable(t, server, owner.AccessToken, testfixture.ClaimBody(device, mine))
	assertEnrollmentBound(t, pool, device.DeviceID, mine)

	_, err = pool.Exec(t.Context(), `UPDATE devices SET claim_expires_at = clock_timestamp() - interval '1 second'
		WHERE id=$1`, expiring.DeviceID)
	require.NoError(t, err)
	setElapsed(24 * time.Hour)
	owner = testfixture.ReadHTTPTokenPair(
		t,
		testfixture.PostRefreshToken(t, server, testfixture.RefreshPath, owner.RefreshToken),
	)
	assertClaimUnavailable(t, server, owner.AccessToken, testfixture.ClaimBody(expiring, mine))
}

func TestClaimHTTPChecksExpiryAfterWaitingForDeviceLock(t *testing.T) {
	t.Parallel()
	assertClaimExpiryAfterLock(t, false)
}

func TestClaimHTTPChecksExpiryAfterWaitingForMachineLock(t *testing.T) {
	t.Parallel()
	assertClaimExpiryAfterLock(t, true)
}

func assertClaimExpiryAfterLock(t *testing.T, lockMachine bool) {
	t.Helper()
	pool := testutil.Postgres(t)
	// Keep time to observe expiry before the access-token middleware deadline.
	const claimTTL = 8 * time.Second
	server := testfixture.NewAuthServer(t, pool, application.Config{
		Device: application.DeviceConfig{ClaimTTL: claimTTL},
	})
	t.Cleanup(server.Close)
	factory := testfixture.StartFactorySession(t, pool, server)
	machineID := testfixture.CreateEnrollmentMachine(t, server, factory.AccessToken)
	value := testfixture.ProvisionEnrollment(t, server, factory.AccessToken)
	tx, err := pool.Begin(t.Context())
	require.NoError(t, err)
	defer tx.Rollback(context.WithoutCancel(t.Context()))
	query, id := "SELECT id FROM devices WHERE id=$1 FOR UPDATE", value.DeviceID
	if lockMachine {
		query, id = "SELECT id FROM machines WHERE id=$1 FOR UPDATE", machineID
	}
	_, err = tx.Exec(t.Context(), query, id)
	require.NoError(t, err)

	type result struct {
		response testfixture.AuthHTTPResponse
		err      error
	}
	completed := make(chan result, 1)
	go func() {
		response, requestErr := testfixture.SendAuthHTTPRequest(
			t.Context(), server.Client(), http.MethodPost, server.URL+testfixture.ClaimPath,
			testfixture.ClaimBody(value, machineID), testfixture.Bearer(factory.AccessToken),
		)
		completed <- result{response: response, err: requestErr}
	}()
	require.EventuallyWithT(t, func(collect *assert.CollectT) {
		var waiting bool
		err := pool.QueryRow(t.Context(), `SELECT EXISTS (
			SELECT 1 FROM pg_stat_activity WHERE $1::int = ANY(pg_blocking_pids(pid))
		)`, int64(tx.Conn().PgConn().PID())).Scan(&waiting)
		require.NoError(collect, err)
		assert.True(collect, waiting)
	}, 3*time.Second, 10*time.Millisecond)
	var expired bool
	require.NoError(t, pool.QueryRow(t.Context(), `SELECT claim_expires_at <= clock_timestamp()
		FROM devices WHERE id=$1`, value.DeviceID).Scan(&expired))
	assert.False(t, expired)
	require.EventuallyWithT(t, func(collect *assert.CollectT) {
		var hasExpired bool
		err := pool.QueryRow(t.Context(), `SELECT claim_expires_at <= clock_timestamp()
			FROM devices WHERE id=$1`, value.DeviceID).Scan(&hasExpired)
		require.NoError(collect, err)
		assert.True(collect, hasExpired)
	}, claimTTL+2*time.Second, 10*time.Millisecond)
	require.NoError(t, tx.Commit(t.Context()))

	attempt := <-completed
	require.NoError(t, attempt.err)
	assert.Equal(t, http.StatusNotFound, attempt.response.Status)
	assert.JSONEq(t, claimError, attempt.response.Body)
	var untouched bool
	require.NoError(t, pool.QueryRow(t.Context(), `SELECT machine_id IS NULL AND claim_verifier IS NOT NULL
		FROM devices WHERE id=$1`, value.DeviceID).Scan(&untouched))
	assert.True(t, untouched)
}

func TestConcurrentHTTPClaimsBindToOneOwner(t *testing.T) {
	t.Parallel()
	pool := testutil.Postgres(t)
	server, _ := testfixture.NewSessionServer(t, pool)
	factory := testfixture.StartFactorySession(t, pool, server)
	_, owner := testfixture.SignupEnrollmentUser(
		t,
		server,
		`{"email":"owner@example.com","password":"long enough password"}`,
	)
	factoryMachine := testfixture.CreateEnrollmentMachine(t, server, factory.AccessToken)
	ownerMachine := testfixture.CreateEnrollmentMachine(t, server, owner.AccessToken)
	value := testfixture.ProvisionEnrollment(t, server, factory.AccessToken)

	type result struct {
		response testfixture.AuthHTTPResponse
		target   string
		err      error
	}
	results := make(chan result, 2)
	start := make(chan struct{})
	var workers sync.WaitGroup
	for _, claimant := range []struct{ token, target string }{
		{factory.AccessToken, factoryMachine},
		{owner.AccessToken, ownerMachine},
	} {
		workers.Go(func() {
			<-start
			response, err := testfixture.SendAuthHTTPRequest(
				t.Context(), server.Client(), http.MethodPost, server.URL+testfixture.ClaimPath,
				testfixture.ClaimBody(value, claimant.target), testfixture.Bearer(claimant.token),
			)
			results <- result{response: response, target: claimant.target, err: err}
		})
	}
	close(start)
	workers.Wait()
	close(results)

	var statuses []int
	for attempt := range results {
		require.NoError(t, attempt.err)
		statuses = append(statuses, attempt.response.Status)
		if attempt.response.Status == http.StatusOK {
			assertClaimResponse(t, attempt.response.Body, value.DeviceID, attempt.target)
			assertEnrollmentBound(t, pool, value.DeviceID, attempt.target)
		} else {
			assert.JSONEq(t, claimError, attempt.response.Body)
		}
	}
	assert.ElementsMatch(t, []int{http.StatusOK, http.StatusNotFound}, statuses)
}

func TestEnrollmentSharesIPBudgetWithAuthentication(t *testing.T) {
	t.Parallel()
	pool := testutil.Postgres(t)
	server := testfixture.NewAuthServer(t, pool, application.Config{
		RateLimits: application.RateLimitsConfig{IP: middleware.RateLimitConfig{Limit: 2}},
	})
	t.Cleanup(server.Close)
	for _, path := range []string{testfixture.ProvisionPath, testfixture.ClaimPath} {
		response := testfixture.AuthHTTPRequest(t, server, http.MethodPost, path, "", nil)
		assert.Equal(t, http.StatusUnauthorized, response.Status)
		assert.Equal(t, "no-store", response.Header.Get("Cache-Control"))
	}
	response := testfixture.AuthHTTPRequest(
		t,
		server,
		http.MethodPost,
		"/v1/auth/login",
		testfixture.SessionSignupBody,
		nil,
	)
	assert.Equal(t, http.StatusTooManyRequests, response.Status)
	response = testfixture.AuthHTTPRequest(t, server, http.MethodPost, testfixture.ProvisionPath, "", nil)
	assert.Equal(t, http.StatusTooManyRequests, response.Status)
	assert.Equal(t, "no-store", response.Header.Get("Cache-Control"))
	assert.NotEmpty(t, response.Header.Get("Retry-After"))
	assertDeviceCount(t, pool, 0)
}

func assertStoredEnrollment(t *testing.T, pool *pgxpool.Pool, value testfixture.FactoryEnrollment, key string) {
	t.Helper()
	rawKey, err := base64.RawURLEncoding.Strict().DecodeString(key)
	require.NoError(t, err)
	assert.Len(t, rawKey, sha256.Size)
	rawCode, err := base64.RawURLEncoding.Strict().DecodeString(value.ClaimPayload.ClaimCode)
	require.NoError(t, err)
	assert.Len(t, rawCode, sha256.Size)
	var storedKey, storedCode []byte
	var createdAt, expiresAt time.Time
	require.NoError(t, pool.QueryRow(t.Context(), `SELECT key_verifier,claim_verifier,created_at,claim_expires_at
		FROM devices WHERE id=$1`, value.DeviceID).Scan(&storedKey, &storedCode, &createdAt, &expiresAt))
	keyHash, claimHash := sha256.Sum256(rawKey), sha256.Sum256(rawCode)
	assert.Equal(t, keyHash[:], storedKey)
	assert.Equal(t, claimHash[:], storedCode)
	assert.NotEqual(t, rawKey, storedKey)
	assert.NotEqual(t, rawCode, storedCode)
	assert.Equal(t, 24*time.Hour, expiresAt.Sub(createdAt))
}

func assertProvisioningForbidden(t *testing.T, server *httptest.Server, token string) {
	t.Helper()
	response := testfixture.AuthHTTPRequest(
		t,
		server,
		http.MethodPost,
		testfixture.ProvisionPath,
		"",
		testfixture.Bearer(token),
	)
	assert.Equal(t, http.StatusForbidden, response.Status)
	assert.Equal(t, "no-store", response.Header.Get("Cache-Control"))
	assert.JSONEq(t, `{"error":{"code":"forbidden","message":"Access denied"}}`, response.Body)
}

func assertDeviceCount(t *testing.T, pool *pgxpool.Pool, expected int) {
	t.Helper()
	var count int
	require.NoError(t, pool.QueryRow(t.Context(), "SELECT count(*) FROM devices").Scan(&count))
	assert.Equal(t, expected, count)
}

func assertClaimUnavailable(t *testing.T, server *httptest.Server, token, body string) {
	t.Helper()
	response := testfixture.AuthHTTPRequest(
		t,
		server,
		http.MethodPost,
		testfixture.ClaimPath,
		body,
		testfixture.Bearer(token),
	)
	assert.Equal(t, http.StatusNotFound, response.Status)
	assert.Equal(t, "no-store", response.Header.Get("Cache-Control"))
	assert.JSONEq(t, claimError, response.Body)
}

func assertClaimResponse(t *testing.T, body, deviceID, machineID string) {
	t.Helper()
	assert.JSONEq(t, `{"device_id":"`+deviceID+`","machine_id":"`+machineID+`","status":"claimed"}`, body)
}

func assertEnrollmentBound(t *testing.T, pool *pgxpool.Pool, deviceID, machineID string) {
	t.Helper()
	var storedMachine string
	var consumed bool
	require.NoError(
		t,
		pool.QueryRow(t.Context(), `SELECT machine_id::text, claim_verifier IS NULL AND claimed_at IS NOT NULL
		FROM devices WHERE id=$1`, deviceID).Scan(&storedMachine, &consumed),
	)
	assert.Equal(t, machineID, storedMachine)
	assert.True(t, consumed)
}
