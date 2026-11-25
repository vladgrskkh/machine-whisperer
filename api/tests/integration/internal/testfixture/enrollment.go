//go:build integration

package testfixture

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vladgrskkh/machine-whisperer/api/internal/domain/device"
	"github.com/vladgrskkh/machine-whisperer/api/internal/domain/machine"
	"github.com/vladgrskkh/machine-whisperer/api/internal/domain/user"
	machinepostgres "github.com/vladgrskkh/machine-whisperer/api/internal/infrastructure/machine/postgres"
	userpostgres "github.com/vladgrskkh/machine-whisperer/api/internal/infrastructure/user/postgres"
)

const (
	ProvisionPath = "/internal/v1/device-provisioning"
	MachinesPath  = "/v1/machines"
	ClaimPath     = "/v1/devices/claim"
)

type FactoryEnrollment struct {
	DeviceID     string `json:"device_id"`
	APIKey       string `json:"api_key"`
	ClaimPayload struct {
		Version   int    `json:"v"`
		DeviceID  string `json:"device_id"`
		ClaimCode string `json:"claim_code"`
	} `json:"claim_payload"`
}

func SignupEnrollmentUser(t *testing.T, server *httptest.Server, credentials string) (string, HTTPTokenPair) {
	t.Helper()
	response := AuthHTTPRequest(t, server, http.MethodPost, "/v1/auth/signup", credentials, nil)
	require.Equal(t, http.StatusCreated, response.Status)
	var account struct {
		ID string `json:"id"`
	}
	require.NoError(t, json.Unmarshal([]byte(response.Body), &account))
	require.NoError(t, uuid.Validate(account.ID))
	pair := ReadHTTPTokenPair(t, AuthHTTPRequest(t, server, http.MethodPost, "/v1/auth/login", credentials, nil))
	return account.ID, pair
}

func StartFactorySession(t *testing.T, pool *pgxpool.Pool, server *httptest.Server) HTTPTokenPair {
	t.Helper()
	id, _ := SignupEnrollmentUser(t, server, SessionSignupBody)
	require.NoError(t, userpostgres.New(pool).GrantRole(t.Context(), id, user.RoleDeviceProvisioner))
	return LoginHTTPSession(t, server)
}

func ProvisionEnrollment(t *testing.T, server *httptest.Server, token string) FactoryEnrollment {
	t.Helper()
	const (
		provisioningFields = 3
		claimPayloadFields = 3
	)
	response := AuthHTTPRequest(t, server, http.MethodPost, ProvisionPath, "", Bearer(token))
	require.Equal(t, http.StatusCreated, response.Status)
	assert.Equal(t, "no-store", response.Header.Get("Cache-Control"))
	var fields, qrFields map[string]json.RawMessage
	require.NoError(t, json.Unmarshal([]byte(response.Body), &fields))
	assert.Len(t, fields, provisioningFields)
	require.NoError(t, json.Unmarshal(fields["claim_payload"], &qrFields))
	assert.Len(t, qrFields, claimPayloadFields)
	var value FactoryEnrollment
	require.NoError(t, json.Unmarshal([]byte(response.Body), &value))
	require.NoError(t, uuid.Validate(value.DeviceID))
	assert.Equal(t, value.DeviceID, value.ClaimPayload.DeviceID)
	assert.Equal(t, 1, value.ClaimPayload.Version)
	return value
}

func EnrollmentKey(t *testing.T, value FactoryEnrollment) string {
	t.Helper()
	id, secret, ok := strings.Cut(value.APIKey, ".")
	require.True(t, ok)
	assert.Equal(t, value.DeviceID, id)
	return secret
}

func CreateEnrollmentMachine(t *testing.T, server *httptest.Server, token string) string {
	t.Helper()
	response := AuthHTTPRequest(t, server, http.MethodPost, MachinesPath, `{"name":"Motor"}`, Bearer(token))
	return ReadEnrollmentMachine(t, response, "Motor")
}

func ReadEnrollmentMachine(t *testing.T, response AuthHTTPResponse, name string) string {
	t.Helper()
	const machineFields = 3
	require.Equal(t, http.StatusCreated, response.Status)
	var fields map[string]json.RawMessage
	require.NoError(t, json.Unmarshal([]byte(response.Body), &fields))
	assert.Len(t, fields, machineFields)
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

func ClaimBody(value FactoryEnrollment, target string) string {
	return `{"device_id":"` + value.DeviceID + `","claim_code":"` + value.ClaimPayload.ClaimCode +
		`","machine_id":"` + target + `"}`
}

func EnrollmentMachines(t *testing.T, pool *pgxpool.Pool) (machine.Machine, machine.Machine) {
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

func EnrollmentDevice(now time.Time) (device.Device, [32]byte, [32]byte) {
	var key, code [32]byte
	rand.Read(key[:])
	rand.Read(code[:])
	return device.Device{
		ID: uuid.NewString(), KeyVerifier: sha256.Sum256(key[:]), ClaimVerifier: sha256.Sum256(code[:]),
		ClaimExpiresAt: now.Add(time.Hour), CreatedAt: now,
	}, key, code
}
