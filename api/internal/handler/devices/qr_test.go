package devices_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vladgrskkh/machine-whisperer/api/internal/handler/devices"
)

const (
	deviceID  = "78efc85a-fd2b-4207-ab7f-8c2c45cf2abc"
	claimCode = "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	qrJSON    = `{"v":1,"device_id":"` + deviceID + `","claim_code":"` + claimCode + `"}`
)

func TestClaimPayloadVersionOne(t *testing.T) {
	t.Parallel()
	value, err := devices.DecodeClaimPayload(strings.NewReader(qrJSON))
	require.NoError(t, err)
	assert.Equal(t, 1, value.Version)
	assert.Equal(t, deviceID, value.DeviceID)
	assert.Equal(t, claimCode, value.ClaimCode)
	encoded, err := json.Marshal(value)
	require.NoError(t, err)
	assert.JSONEq(t, qrJSON, string(encoded))
}

func TestClaimPayloadRejectsInvalidInput(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ name, body string }{
		{"malformed", qrJSON[:len(qrJSON)-2]},
		{"wrong version", strings.Replace(qrJSON, `"v":1`, `"v":2`, 1)},
		{"string version", strings.Replace(qrJSON, `"v":1`, `"v":"1"`, 1)},
		{"null version", strings.Replace(qrJSON, `"v":1`, `"v":null`, 1)},
		{"missing version", "{" + strings.SplitN(qrJSON, ",", 2)[1]},
		{"unknown field", `{"api_key":"secret",` + qrJSON[1:]},
		{"duplicate field", `{"v":1,` + qrJSON[1:]},
		{"escaped duplicate", `{"\u0076":1,` + qrJSON[1:]},
		{"mixed case field", strings.Replace(qrJSON, "device_id", "Device_ID", 1)},
		{"mixed case duplicate", `{"V":1,` + qrJSON[1:]},
		{"oversized", qrJSON + strings.Repeat(" ", 8192)},
		{"trailing data", qrJSON + " {}"},
		{"invalid UUID", strings.Replace(qrJSON, deviceID, "missing-uuid", 1)},
		{"short code", strings.Replace(qrJSON, claimCode, claimCode[1:], 1)},
		{"padding", strings.Replace(qrJSON, claimCode, claimCode+"=", 1)},
		{"standard alphabet", strings.Replace(qrJSON, claimCode, "+"+claimCode[1:], 1)},
		{"newline", strings.Replace(qrJSON, claimCode, `\n`+claimCode[1:], 1)},
		{"noncanonical trailing bits", strings.Replace(qrJSON, claimCode, claimCode[1:]+"B", 1)},
		{"null object", "null"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			payload, err := devices.DecodeClaimPayload(strings.NewReader(tc.body))
			assert.ErrorIs(t, err, devices.ErrInvalidClaimPayload)
			assert.Zero(t, payload)
		})
	}
}
