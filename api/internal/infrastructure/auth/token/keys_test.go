package token_test

import (
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vladgrskkh/machine-whisperer/api/internal/infrastructure/auth/token"
)

func keyFiles(t *testing.T, keys token.Keys) (string, string) {
	t.Helper()
	dir := t.TempDir()
	privateFile, publicFile := filepath.Join(dir, "private.pem"), filepath.Join(dir, "public.json")
	der, err := x509.MarshalPKCS8PrivateKey(keys.SigningKey)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(privateFile, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}), 0600))
	values := make(map[string]string, len(keys.VerifyKeys))
	for id, key := range keys.VerifyKeys {
		values[id] = base64.StdEncoding.EncodeToString(key)
	}
	raw, err := json.Marshal(values)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(publicFile, raw, 0600))
	return privateFile, publicFile
}

func TestLoadKeysAndRotation(t *testing.T) {
	t.Parallel()
	old, now, claims := fixture(t)
	oldAdapter, err := token.NewJWT(old, func() time.Time { return now })
	require.NoError(t, err)
	oldRaw, err := oldAdapter.Sign(t.Context(), claims)
	require.NoError(t, err)
	public, private, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	keys := token.Keys{
		SigningKeyID: "new",
		SigningKey:   private,
		VerifyKeys:   map[string]ed25519.PublicKey{"new": public, "active": old.VerifyKeys["active"]},
	}
	signingFile, verifyFile := keyFiles(t, keys)
	loaded, err := token.LoadKeys("new", signingFile, verifyFile)
	require.NoError(t, err)
	adapter, err := token.NewJWT(loaded, func() time.Time { return now })
	require.NoError(t, err)
	_, err = adapter.Verify(t.Context(), oldRaw)
	require.NoError(t, err)
	delete(keys.VerifyKeys, "active")
	signingFile, verifyFile = keyFiles(t, keys)
	loaded, err = token.LoadKeys("new", signingFile, verifyFile)
	require.NoError(t, err)
	adapter, err = token.NewJWT(loaded, func() time.Time { return now })
	require.NoError(t, err)
	_, err = adapter.Verify(t.Context(), oldRaw)
	assert.ErrorIs(t, err, token.ErrInvalidToken)
}

func TestInvalidKeyFiles(t *testing.T) {
	t.Parallel()
	keys, _, _ := fixture(t)
	signingFile, verifyFile := keyFiles(t, keys)
	for _, tt := range []struct{ name, id, sign, verify string }{
		{"missing signing", "active", "/missing/private", verifyFile},
		{"missing verify", "active", signingFile, "/missing/public"},
		{"empty id", "", signingFile, verifyFile},
		{"unknown active", "unknown", signingFile, verifyFile},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, err := token.LoadKeys(tt.id, tt.sign, tt.verify)
			assert.ErrorIs(t, err, token.ErrInvalidKeys)
		})
	}
	for _, tt := range []struct{ name, private, public string }{
		{"bad PEM", "not pem", `{}`},
		{"bad JSON", "", `broken`},
		{"wrong length", "", `{"active":"AQ=="}`},
		{"invalid encoding", "", `{"active":"?"}`},
		{"empty id", "", `{"":"AQ=="}`},
		{"trailing JSON", "", `{} {}`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			sign, verify := keyFiles(t, keys)
			if tt.private != "" {
				require.NoError(t, os.WriteFile(sign, []byte(tt.private), 0600))
			}
			require.NoError(t, os.WriteFile(verify, []byte(tt.public), 0600))
			_, err := token.LoadKeys("active", sign, verify)
			assert.ErrorIs(t, err, token.ErrInvalidKeys)
		})
	}
}

func TestNewJWTRejectsInvalidKeys(t *testing.T) {
	t.Parallel()
	keys, now, _ := fixture(t)
	bad := []token.Keys{
		{},
		{SigningKeyID: "active", SigningKey: keys.SigningKey, VerifyKeys: map[string]ed25519.PublicKey{"active": {1}}},
	}
	other, _, _ := fixture(t)
	bad = append(bad, token.Keys{SigningKeyID: "active", SigningKey: keys.SigningKey, VerifyKeys: other.VerifyKeys})
	for _, value := range bad {
		_, err := token.NewJWT(value, func() time.Time { return now })
		assert.ErrorIs(t, err, token.ErrInvalidKeys)
	}
}

func TestLoadKeysRejectsWrongPrivateKeyType(t *testing.T) {
	t.Parallel()
	keys, _, _ := fixture(t)
	sign, verify := keyFiles(t, keys)
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	der, err := x509.MarshalPKCS8PrivateKey(key)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(sign, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}), 0600))
	_, err = token.LoadKeys("active", sign, verify)
	assert.ErrorIs(t, err, token.ErrInvalidKeys)
}

func TestLoadKeysRejectsMismatchedPair(t *testing.T) {
	t.Parallel()
	keys, _, _ := fixture(t)
	other, _, _ := fixture(t)
	keys.VerifyKeys = other.VerifyKeys
	sign, verify := keyFiles(t, keys)
	_, err := token.LoadKeys("active", sign, verify)
	assert.ErrorIs(t, err, token.ErrInvalidKeys)
}
