//go:build integration

package enrollment_test

import (
	"net/http"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vladgrskkh/machine-whisperer/api/internal/testutil"
	"github.com/vladgrskkh/machine-whisperer/api/tests/integration/internal/testfixture"
)

func TestMachineDeleteIsOwnerScopedAndIdempotent(t *testing.T) {
	t.Parallel()
	pool := testutil.Postgres(t)
	server, _ := testfixture.NewSessionServer(t, pool)
	_, owner := testfixture.SignupEnrollmentUser(t, server, testfixture.SessionSignupBody)
	_, other := testfixture.SignupEnrollmentUser(t, server,
		`{"email":"other@example.com","password":"long enough password"}`)
	ownerAuth := testfixture.Bearer(owner.AccessToken)
	otherAuth := testfixture.Bearer(other.AccessToken)
	machineID := testfixture.CreateEnrollmentMachine(t, server, owner.AccessToken)
	siblingID := testfixture.CreateEnrollmentMachine(t, server, owner.AccessToken)
	path := testfixture.MachinesPath + "/" + machineID

	unauthenticated := testfixture.AuthHTTPRequest(t, server, http.MethodDelete, path, "", nil)
	assert.Equal(t, http.StatusUnauthorized, unauthenticated.Status)
	assert.Equal(t, "no-store", unauthenticated.Header.Get("Cache-Control"))
	assert.Equal(t, "Bearer", unauthenticated.Header.Get("WWW-Authenticate"))
	assert.JSONEq(t, testfixture.AuthFailedJSON, unauthenticated.Body)
	malformed := testfixture.AuthHTTPRequest(t, server, http.MethodDelete,
		testfixture.MachinesPath+"/not-a-uuid", "", ownerAuth)
	assert.Equal(t, http.StatusBadRequest, malformed.Status)
	assert.Contains(t, malformed.Body, `"invalid_input"`)

	missing := testfixture.AuthHTTPRequest(t, server, http.MethodDelete,
		testfixture.MachinesPath+"/"+uuid.NewString(), "", ownerAuth)
	foreign := testfixture.AuthHTTPRequest(t, server, http.MethodDelete, path, "", otherAuth)
	require.Equal(t, http.StatusNotFound, missing.Status)
	assert.Equal(t, http.StatusNotFound, foreign.Status)
	assert.JSONEq(t, `{"error":{"code":"machine_not_found","message":"Machine not found"}}`, missing.Body)
	assert.JSONEq(t, missing.Body, foreign.Body)
	before := testfixture.AuthHTTPRequest(t, server, http.MethodGet, path, "", ownerAuth)
	require.Equal(t, http.StatusOK, before.Status)

	first := testfixture.AuthHTTPRequest(t, server, http.MethodDelete, path, "", ownerAuth)
	require.Equal(t, http.StatusNoContent, first.Status, first.Body)
	assert.Empty(t, first.Body)
	assert.Equal(t, "no-store", first.Header.Get("Cache-Control"))
	const archivedAt = "SELECT deleted_at FROM machines WHERE id=$1"
	var firstDeletion, retryDeletion time.Time
	require.NoError(t, pool.QueryRow(t.Context(), archivedAt, machineID).Scan(&firstDeletion))
	assert.False(t, firstDeletion.IsZero())

	retry := testfixture.AuthHTTPRequest(t, server, http.MethodDelete, path, "", ownerAuth)
	assert.Equal(t, http.StatusNoContent, retry.Status)
	assert.Empty(t, retry.Body)
	require.NoError(t, pool.QueryRow(t.Context(), archivedAt, machineID).Scan(&retryDeletion))
	assert.WithinDuration(t, firstDeletion, retryDeletion, 0)
	foreign = testfixture.AuthHTTPRequest(t, server, http.MethodDelete, path, "", otherAuth)
	assert.Equal(t, http.StatusNotFound, foreign.Status)
	assert.JSONEq(t, missing.Body, foreign.Body)

	detail := testfixture.AuthHTTPRequest(t, server, http.MethodGet, path, "", ownerAuth)
	assert.Equal(t, http.StatusNotFound, detail.Status)
	assert.JSONEq(t, missing.Body, detail.Body)
	list := testfixture.AuthHTTPRequest(t, server, http.MethodGet, testfixture.MachinesPath, "", ownerAuth)
	require.Equal(t, http.StatusOK, list.Status)
	assert.NotContains(t, list.Body, machineID)
	assert.Contains(t, list.Body, siblingID)
}

func TestMachineDeleteRejectsNewClaimsWithoutConsumingCode(t *testing.T) {
	t.Parallel()
	pool := testutil.Postgres(t)
	server, _ := testfixture.NewSessionServer(t, pool)
	factory := testfixture.StartFactorySession(t, pool, server)
	auth := testfixture.Bearer(factory.AccessToken)
	machineID := testfixture.CreateEnrollmentMachine(t, server, factory.AccessToken)
	activeID := testfixture.CreateEnrollmentMachine(t, server, factory.AccessToken)
	dev := testfixture.ProvisionEnrollment(t, server, factory.AccessToken)

	path := testfixture.MachinesPath + "/" + machineID
	deleted := testfixture.AuthHTTPRequest(t, server, http.MethodDelete, path, "", auth)
	require.Equal(t, http.StatusNoContent, deleted.Status, deleted.Body)
	assertClaimUnavailable(t, server, factory.AccessToken, testfixture.ClaimBody(dev, machineID))
	claimed := testfixture.AuthHTTPRequest(t, server, http.MethodPost, testfixture.ClaimPath,
		testfixture.ClaimBody(dev, activeID), auth)
	require.Equal(t, http.StatusOK, claimed.Status, claimed.Body)
	assertClaimResponse(t, claimed.Body, dev.DeviceID, activeID)
}
