package token

import (
	"bytes"
	"crypto/ed25519"
	"crypto/subtle"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"os"
)

func validKeys(keys Keys) bool {
	if keys.SigningKeyID == "" || len(keys.SigningKey) != ed25519.PrivateKeySize {
		return false
	}
	derived := ed25519.NewKeyFromSeed(keys.SigningKey.Seed())
	if subtle.ConstantTimeCompare(derived, keys.SigningKey) != 1 {
		return false
	}
	for id, key := range keys.VerifyKeys {
		if id == "" || len(key) != ed25519.PublicKeySize {
			return false
		}
	}
	return subtle.ConstantTimeCompare(keys.VerifyKeys[keys.SigningKeyID], derived[ed25519.SeedSize:]) == 1
}

func LoadKeys(activeID, signingFile, verifyFile string) (Keys, error) {
	privatePEM, err := os.ReadFile(signingFile)
	if err != nil {
		return Keys{}, ErrInvalidKeys
	}
	block, rest := pem.Decode(privatePEM)
	if block == nil || block.Type != "PRIVATE KEY" || len(bytes.TrimSpace(rest)) != 0 {
		return Keys{}, ErrInvalidKeys
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return Keys{}, ErrInvalidKeys
	}
	private, ok := parsed.(ed25519.PrivateKey)
	if !ok {
		return Keys{}, ErrInvalidKeys
	}
	raw, err := os.ReadFile(verifyFile)
	if err != nil {
		return Keys{}, ErrInvalidKeys
	}
	var encoded map[string]string
	if err = json.Unmarshal(raw, &encoded); err != nil {
		return Keys{}, ErrInvalidKeys
	}
	keys := Keys{
		SigningKeyID: activeID,
		SigningKey:   private,
		VerifyKeys:   make(map[string]ed25519.PublicKey, len(encoded)),
	}
	for id, value := range encoded {
		public, decodeErr := base64.StdEncoding.Strict().DecodeString(value)
		if decodeErr != nil {
			return Keys{}, ErrInvalidKeys
		}
		keys.VerifyKeys[id] = ed25519.PublicKey(public)
	}
	if !validKeys(keys) {
		return Keys{}, ErrInvalidKeys
	}
	return keys, nil
}
