package testutil

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// AuthKeys provisions ephemeral key files for application tests.
func AuthKeys(t *testing.T) (string, string, string, ed25519.PrivateKey) {
	t.Helper()
	public, private, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	der, err := x509.MarshalPKCS8PrivateKey(private)
	require.NoError(t, err)
	directory := t.TempDir()
	signing := filepath.Join(directory, "signing.pem")
	verifying := filepath.Join(directory, "verifying.json")
	require.NoError(t, os.WriteFile(signing, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}), 0o600))
	data, err := json.Marshal(map[string]string{"active": base64.StdEncoding.EncodeToString(public)})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(verifying, data, 0o600))
	return "active", signing, verifying, private
}
