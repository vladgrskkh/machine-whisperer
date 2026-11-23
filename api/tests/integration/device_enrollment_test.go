//go:build integration

package integration_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
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

	"github.com/vladgrskkh/machine-whisperer/api/internal/application"
	"github.com/vladgrskkh/machine-whisperer/api/internal/domain/user"
	"github.com/vladgrskkh/machine-whisperer/api/internal/handler/devices"
	"github.com/vladgrskkh/machine-whisperer/api/internal/handler/middleware"
	"github.com/vladgrskkh/machine-whisperer/api/internal/infrastructure/auth/token"
	"github.com/vladgrskkh/machine-whisperer/api/internal/infrastructure/device/credential"
	devicepostgres "github.com/vladgrskkh/machine-whisperer/api/internal/infrastructure/device/postgres"
	userpostgres "github.com/vladgrskkh/machine-whisperer/api/internal/infrastructure/user/postgres"
	"github.com/vladgrskkh/machine-whisperer/api/internal/testutil"
	deviceusecase "github.com/vladgrskkh/machine-whisperer/api/internal/usecase/device"
)

const (
	provisionPath = "/internal/v1/device-provisioning"
	machinesPath  = "/v1/machines"
	claimPath     = "/v1/devices/claim"
	claimError    = `{"error":{"code":"claim_unavailable","message":"Device claim unavailable"}}`
)

type factoryEnrollment struct {
	DeviceID     string `json:"device_id"`
	APIKey       string `json:"api_key"`
	ClaimPayload struct {
		Version   int    `json:"v"`
		DeviceID  string `json:"device_id"`
		ClaimCode string `json:"claim_code"`
	} `json:"claim_payload"`
}

func TestProvisioningRequiresSignedRoleAndStoresOnlyVerifiers(t *testing.T) {
	t.Parallel()
	pool := testutil.Postgres(t)
	server, _ := newSessionServer(t, pool)
	id, ordinary := signupEnrollmentUser(t, server, httpCredentials)

	for _, auth := range []http.Header{nil, bearer("invalid-jwt")} {
		response := authHTTPRequest(t, server, http.MethodPost, provisionPath, "", auth)
		assert.Equal(t, http.StatusUnauthorized, response.status)
		assert.Equal(t, "no-store", response.header.Get("Cache-Control"))
		assert.Equal(t, "Bearer", response.header.Get("WWW-Authenticate"))
		assert.JSONEq(t, authFailedJSON, response.body)
	}
	assertProvisioningForbidden(t, server, ordinary.AccessToken)
	assertDeviceCount(t, pool, 0)

	roles := userpostgres.New(pool)
	require.NoError(t, roles.GrantRole(t.Context(), id, user.RoleDeviceProvisioner))
	assertProvisioningForbidden(t, server, ordinary.AccessToken)
	allowed := loginHTTPSession(t, server)
	first := provisionEnrollment(t, server, allowed.AccessToken)
	second := provisionEnrollment(t, server, allowed.AccessToken)
	assert.NotEqual(t, first.DeviceID, second.DeviceID)
	firstKey := enrollmentKey(t, first)
	secondKey := enrollmentKey(t, second)
	assert.NotEqual(t, firstKey, secondKey)
	assert.NotEqual(t, first.ClaimPayload.ClaimCode, second.ClaimPayload.ClaimCode)
	assert.NotEqual(t, firstKey, first.ClaimPayload.ClaimCode)
	assert.NotEqual(t, secondKey, second.ClaimPayload.ClaimCode)
	assertStoredEnrollment(t, pool, first, firstKey)
	assertStoredEnrollment(t, pool, second, secondKey)
	assertDeviceCount(t, pool, 2)
}

func TestProvisioningRoleRevocationFollowsAccessTokenExpiry(t *testing.T) {
	t.Parallel()
	pool := testutil.Postgres(t)
	server, setElapsed := newSessionServer(t, pool)
	id, ordinary := signupEnrollmentUser(t, server, httpCredentials)
	roles := userpostgres.New(pool)
	require.NoError(t, roles.GrantRole(t.Context(), id, user.RoleDeviceProvisioner))
	allowed := readHTTPTokenPair(t, postRefreshToken(t, server, refreshPath, ordinary.RefreshToken))
	provisionEnrollment(t, server, allowed.AccessToken)
	require.NoError(t, roles.RevokeRole(t.Context(), id, user.RoleDeviceProvisioner))
	refreshed := readHTTPTokenPair(t, postRefreshToken(t, server, refreshPath, allowed.RefreshToken))
	assertProvisioningForbidden(t, server, refreshed.AccessToken)
	setElapsed(15*time.Minute + 29*time.Second)
	provisionEnrollment(t, server, allowed.AccessToken)
	setElapsed(15*time.Minute + 31*time.Second)
	response := authHTTPRequest(t, server, http.MethodPost, provisionPath, "", bearer(allowed.AccessToken))
	assert.Equal(t, http.StatusUnauthorized, response.status)
	assert.Equal(t, "no-store", response.header.Get("Cache-Control"))
	assert.JSONEq(t, authFailedJSON, response.body)
	assertDeviceCount(t, pool, 2)
}

func TestProvisioningDatabaseFailureIsSanitized(t *testing.T) {
	t.Parallel()
	pool := testutil.Postgres(t)
	server, _ := newSessionServer(t, pool)
	factory := startFactorySession(t, pool, server)
	_, err := pool.Exec(t.Context(), `ALTER TABLE devices ADD CONSTRAINT internal_enrollment_failure CHECK (false)`)
	require.NoError(t, err)
	response := authHTTPRequest(t, server, http.MethodPost, provisionPath, "", bearer(factory.AccessToken))
	assert.Equal(t, http.StatusInternalServerError, response.status)
	assert.Equal(t, "no-store", response.header.Get("Cache-Control"))
	assert.JSONEq(t, internalAuthErrorJSON, response.body)
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
	)
	var logs bytes.Buffer
	server := httptest.NewUnstartedServer(middleware.NoStore(devices.NewProvision(usecase)))
	server.Config.ErrorLog = slog.NewLogLogger(slog.NewTextHandler(&logs, nil), slog.LevelError)
	server.Start()
	t.Cleanup(server.Close)

	response := authHTTPRequest(t, server, http.MethodPost, provisionPath, "", nil)
	server.Close()
	assert.Equal(t, http.StatusInternalServerError, response.status)
	assert.Equal(t, "no-store", response.header.Get("Cache-Control"))
	assert.JSONEq(t, internalAuthErrorJSON, response.body)
	assert.Empty(t, logs.String())
	assertDeviceCount(t, pool, 0)
}

func TestMachineHTTPOwnershipAndValidation(t *testing.T) {
	t.Parallel()
	pool := testutil.Postgres(t)
	server, _ := newSessionServer(t, pool)
	_, owner := signupEnrollmentUser(t, server, httpCredentials)
	_, other := signupEnrollmentUser(t, server, `{"email":"other@example.com","password":"long enough password"}`)

	response := authHTTPRequest(t, server, http.MethodGet, machinesPath, "", nil)
	assert.Equal(t, http.StatusUnauthorized, response.status)
	response = authHTTPRequest(t, server, http.MethodGet, machinesPath, "", bearer(other.AccessToken))
	assert.Equal(t, http.StatusOK, response.status)
	assert.JSONEq(t, "[]", response.body)

	created := authHTTPRequest(
		t,
		server,
		http.MethodPost,
		machinesPath,
		`{"name":"  Pump \u00e9  "}`,
		bearer(owner.AccessToken),
	)
	machineID := readEnrollmentMachine(t, created, "Pump \u00e9")
	response = authHTTPRequest(t, server, http.MethodGet, machinesPath+"/"+machineID, "", bearer(owner.AccessToken))
	assert.Equal(t, http.StatusOK, response.status)
	assert.JSONEq(t, created.body, response.body)
	response = authHTTPRequest(t, server, http.MethodGet, machinesPath, "", bearer(owner.AccessToken))
	assert.Equal(t, http.StatusOK, response.status)
	assert.JSONEq(t, "["+created.body+"]", response.body)

	for _, id := range []string{machineID, uuid.NewString()} {
		response = authHTTPRequest(t, server, http.MethodGet, machinesPath+"/"+id, "", bearer(other.AccessToken))
		assert.Equal(t, http.StatusNotFound, response.status)
		assert.JSONEq(t, `{"error":{"code":"machine_not_found","message":"Machine not found"}}`, response.body)
	}
	response = authHTTPRequest(t, server, http.MethodGet, machinesPath, "", bearer(other.AccessToken))
	assert.Equal(t, http.StatusOK, response.status)
	assert.JSONEq(t, "[]", response.body)

	for _, body := range []string{
		`{"name":null}`, `{"name":" "}`, `{"name":"motor\u0000"}`, `{"name":"motor\ud800"}`,
		`{"name":"motor","owner_user_id":"someone else"}`, `{"name":"one","name":"two"}`,
		`{"Name":"motor"}`, `{"name":"` + strings.Repeat("x", 101) + `"}`,
	} {
		response = authHTTPRequest(t, server, http.MethodPost, machinesPath, body, bearer(owner.AccessToken))
		assert.Equal(t, http.StatusBadRequest, response.status)
	}
	response = authHTTPRequest(t, server, http.MethodGet, machinesPath+"/invalid-id", "", bearer(owner.AccessToken))
	assert.Equal(t, http.StatusBadRequest, response.status)
}

func TestClaimHTTPIsAtomicAndUniform(t *testing.T) {
	t.Parallel()
	pool := testutil.Postgres(t)
	server, setElapsed := newSessionServer(t, pool)
	factory := startFactorySession(t, pool, server)
	_, owner := signupEnrollmentUser(t, server, `{"email":"owner@example.com","password":"long enough password"}`)
	device := provisionEnrollment(t, server, factory.AccessToken)
	expiring := provisionEnrollment(t, server, factory.AccessToken)
	revoked := provisionEnrollment(t, server, factory.AccessToken)
	foreign := createEnrollmentMachine(t, server, factory.AccessToken)
	mine := createEnrollmentMachine(t, server, owner.AccessToken)
	_, err := pool.Exec(t.Context(), "UPDATE devices SET revoked_at=created_at WHERE id=$1", revoked.DeviceID)
	require.NoError(t, err)

	response := authHTTPRequest(t, server, http.MethodPost, claimPath, claimBody(device, mine), bearer(device.APIKey))
	assert.Equal(t, http.StatusUnauthorized, response.status)
	response = authHTTPRequest(t, server, http.MethodGet, machinesPath, "", bearer(device.APIKey))
	assert.Equal(t, http.StatusUnauthorized, response.status)

	for _, body := range []string{
		claimBody(device, foreign),
		claimBody(device, uuid.NewString()),
		claimBody(revoked, mine),
		strings.Replace(claimBody(device, mine), device.DeviceID, uuid.NewString(), 1),
		strings.Replace(claimBody(device, mine), device.ClaimPayload.ClaimCode, unknownRefreshToken, 1),
	} {
		assertClaimUnavailable(t, server, owner.AccessToken, body)
	}
	for _, body := range []string{
		`{"device_id":"invalid","machine_id":"` + mine + `","claim_code":"` + unknownRefreshToken + `"}`,
		strings.Replace(claimBody(device, mine), device.ClaimPayload.ClaimCode, device.APIKey, 1),
		claimBody(device, mine) + " {}",
		claimBody(device, mine) + strings.Repeat(" ", 8192),
		`{"claim_code":"duplicate",` + claimBody(device, mine)[1:],
	} {
		response = authHTTPRequest(t, server, http.MethodPost, claimPath, body, bearer(owner.AccessToken))
		assert.Equal(t, http.StatusBadRequest, response.status)
		assert.NotContains(t, response.body, device.ClaimPayload.ClaimCode)
		assert.NotContains(t, response.body, device.APIKey)
	}
	_, err = pool.Exec(t.Context(), "ALTER TABLE devices ADD CONSTRAINT refuse_claim CHECK (claimed_at IS NULL)")
	require.NoError(t, err)
	response = authHTTPRequest(
		t,
		server,
		http.MethodPost,
		claimPath,
		claimBody(device, mine),
		bearer(owner.AccessToken),
	)
	assert.Equal(t, http.StatusInternalServerError, response.status)
	assert.JSONEq(t, internalAuthErrorJSON, response.body)
	_, err = pool.Exec(t.Context(), "ALTER TABLE devices DROP CONSTRAINT refuse_claim")
	require.NoError(t, err)
	response = authHTTPRequest(
		t,
		server,
		http.MethodPost,
		claimPath,
		claimBody(device, mine),
		bearer(owner.AccessToken),
	)
	require.Equal(t, http.StatusOK, response.status)
	assert.Equal(t, "no-store", response.header.Get("Cache-Control"))
	assertClaimResponse(t, response.body, device.DeviceID, mine)
	assertClaimUnavailable(t, server, owner.AccessToken, claimBody(device, mine))
	assertEnrollmentBound(t, pool, device.DeviceID, mine)

	_, err = pool.Exec(t.Context(), `UPDATE devices SET claim_expires_at = clock_timestamp() - interval '1 second'
		WHERE id=$1`, expiring.DeviceID)
	require.NoError(t, err)
	setElapsed(24 * time.Hour)
	owner = readHTTPTokenPair(t, postRefreshToken(t, server, refreshPath, owner.RefreshToken))
	assertClaimUnavailable(t, server, owner.AccessToken, claimBody(expiring, mine))
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
	server := newAuthServer(t, pool, application.Config{
		Device: application.DeviceConfig{ClaimTTL: claimTTL},
	})
	t.Cleanup(server.Close)
	factory := startFactorySession(t, pool, server)
	machineID := createEnrollmentMachine(t, server, factory.AccessToken)
	value := provisionEnrollment(t, server, factory.AccessToken)
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
		response authHTTPResponse
		err      error
	}
	completed := make(chan result, 1)
	go func() {
		response, requestErr := sendAuthHTTPRequest(
			t.Context(), server.Client(), http.MethodPost, server.URL+claimPath,
			claimBody(value, machineID), bearer(factory.AccessToken),
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
	assert.Equal(t, http.StatusNotFound, attempt.response.status)
	assert.JSONEq(t, claimError, attempt.response.body)
	var untouched bool
	require.NoError(t, pool.QueryRow(t.Context(), `SELECT machine_id IS NULL AND claim_verifier IS NOT NULL
		FROM devices WHERE id=$1`, value.DeviceID).Scan(&untouched))
	assert.True(t, untouched)
}

func TestConcurrentHTTPClaimsBindToOneOwner(t *testing.T) {
	t.Parallel()
	pool := testutil.Postgres(t)
	server, _ := newSessionServer(t, pool)
	factory := startFactorySession(t, pool, server)
	_, owner := signupEnrollmentUser(t, server, `{"email":"owner@example.com","password":"long enough password"}`)
	factoryMachine := createEnrollmentMachine(t, server, factory.AccessToken)
	ownerMachine := createEnrollmentMachine(t, server, owner.AccessToken)
	value := provisionEnrollment(t, server, factory.AccessToken)

	type result struct {
		response authHTTPResponse
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
			response, err := sendAuthHTTPRequest(
				t.Context(), server.Client(), http.MethodPost, server.URL+claimPath,
				claimBody(value, claimant.target), bearer(claimant.token),
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
		statuses = append(statuses, attempt.response.status)
		if attempt.response.status == http.StatusOK {
			assertClaimResponse(t, attempt.response.body, value.DeviceID, attempt.target)
			assertEnrollmentBound(t, pool, value.DeviceID, attempt.target)
		} else {
			assert.JSONEq(t, claimError, attempt.response.body)
		}
	}
	assert.ElementsMatch(t, []int{http.StatusOK, http.StatusNotFound}, statuses)
}

func TestEnrollmentSharesIPBudgetWithAuthentication(t *testing.T) {
	t.Parallel()
	pool := testutil.Postgres(t)
	server := newAuthServer(t, pool, application.Config{
		RateLimits: application.RateLimitsConfig{IP: middleware.RateLimitConfig{Limit: 2}},
	})
	t.Cleanup(server.Close)
	for _, path := range []string{provisionPath, claimPath} {
		response := authHTTPRequest(t, server, http.MethodPost, path, "", nil)
		assert.Equal(t, http.StatusUnauthorized, response.status)
		assert.Equal(t, "no-store", response.header.Get("Cache-Control"))
	}
	response := authHTTPRequest(t, server, http.MethodPost, "/v1/auth/login", httpCredentials, nil)
	assert.Equal(t, http.StatusTooManyRequests, response.status)
	response = authHTTPRequest(t, server, http.MethodPost, provisionPath, "", nil)
	assert.Equal(t, http.StatusTooManyRequests, response.status)
	assert.Equal(t, "no-store", response.header.Get("Cache-Control"))
	assert.NotEmpty(t, response.header.Get("Retry-After"))
	assertDeviceCount(t, pool, 0)
}

func signupEnrollmentUser(t *testing.T, server *httptest.Server, credentials string) (string, httpTokenPair) {
	t.Helper()
	response := authHTTPRequest(t, server, http.MethodPost, "/v1/auth/signup", credentials, nil)
	require.Equal(t, http.StatusCreated, response.status)
	var account struct {
		ID string `json:"id"`
	}
	require.NoError(t, json.Unmarshal([]byte(response.body), &account))
	require.NoError(t, uuid.Validate(account.ID))
	pair := readHTTPTokenPair(t, authHTTPRequest(t, server, http.MethodPost, "/v1/auth/login", credentials, nil))
	return account.ID, pair
}

func startFactorySession(t *testing.T, pool *pgxpool.Pool, server *httptest.Server) httpTokenPair {
	t.Helper()
	id, _ := signupEnrollmentUser(t, server, httpCredentials)
	require.NoError(t, userpostgres.New(pool).GrantRole(t.Context(), id, user.RoleDeviceProvisioner))
	return loginHTTPSession(t, server)
}

func bearer(token string) http.Header {
	return http.Header{"Authorization": {"Bearer " + token}}
}

func provisionEnrollment(t *testing.T, server *httptest.Server, token string) factoryEnrollment {
	t.Helper()
	response := authHTTPRequest(t, server, http.MethodPost, provisionPath, "", bearer(token))
	require.Equal(t, http.StatusCreated, response.status)
	assert.Equal(t, "no-store", response.header.Get("Cache-Control"))
	var fields, qrFields map[string]json.RawMessage
	require.NoError(t, json.Unmarshal([]byte(response.body), &fields))
	assert.Len(t, fields, 3)
	require.NoError(t, json.Unmarshal(fields["claim_payload"], &qrFields))
	assert.Len(t, qrFields, 3)
	var value factoryEnrollment
	require.NoError(t, json.Unmarshal([]byte(response.body), &value))
	require.NoError(t, uuid.Validate(value.DeviceID))
	assert.Equal(t, value.DeviceID, value.ClaimPayload.DeviceID)
	assert.Equal(t, 1, value.ClaimPayload.Version)
	return value
}

func enrollmentKey(t *testing.T, value factoryEnrollment) string {
	t.Helper()
	id, secret, ok := strings.Cut(value.APIKey, ".")
	require.True(t, ok)
	assert.Equal(t, value.DeviceID, id)
	return secret
}

func assertStoredEnrollment(t *testing.T, pool *pgxpool.Pool, value factoryEnrollment, key string) {
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
	response := authHTTPRequest(t, server, http.MethodPost, provisionPath, "", bearer(token))
	assert.Equal(t, http.StatusForbidden, response.status)
	assert.Equal(t, "no-store", response.header.Get("Cache-Control"))
	assert.JSONEq(t, `{"error":{"code":"forbidden","message":"Access denied"}}`, response.body)
}

func assertDeviceCount(t *testing.T, pool *pgxpool.Pool, expected int) {
	t.Helper()
	var count int
	require.NoError(t, pool.QueryRow(t.Context(), "SELECT count(*) FROM devices").Scan(&count))
	assert.Equal(t, expected, count)
}

func createEnrollmentMachine(t *testing.T, server *httptest.Server, token string) string {
	t.Helper()
	response := authHTTPRequest(t, server, http.MethodPost, machinesPath, `{"name":"Motor"}`, bearer(token))
	return readEnrollmentMachine(t, response, "Motor")
}

func readEnrollmentMachine(t *testing.T, response authHTTPResponse, name string) string {
	t.Helper()
	require.Equal(t, http.StatusCreated, response.status)
	var fields map[string]json.RawMessage
	require.NoError(t, json.Unmarshal([]byte(response.body), &fields))
	assert.Len(t, fields, 3)
	var id, displayName string
	var createdAt time.Time
	require.NoError(t, json.Unmarshal(fields["id"], &id))
	require.NoError(t, uuid.Validate(id))
	require.NoError(t, json.Unmarshal(fields["name"], &displayName))
	require.NoError(t, json.Unmarshal(fields["created_at"], &createdAt))
	assert.Equal(t, name, displayName)
	assert.False(t, createdAt.IsZero())
	return id
}

func claimBody(value factoryEnrollment, target string) string {
	return `{"device_id":"` + value.DeviceID + `","claim_code":"` + value.ClaimPayload.ClaimCode +
		`","machine_id":"` + target + `"}`
}

func assertClaimUnavailable(t *testing.T, server *httptest.Server, token, body string) {
	t.Helper()
	response := authHTTPRequest(t, server, http.MethodPost, claimPath, body, bearer(token))
	assert.Equal(t, http.StatusNotFound, response.status)
	assert.Equal(t, "no-store", response.header.Get("Cache-Control"))
	assert.JSONEq(t, claimError, response.body)
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
