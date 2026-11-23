package device

import (
	"crypto/sha256"
	"encoding/base64"
	"errors"
)

const secretBytes = 32

var ErrInvalidSecret = errors.New("invalid device secret")

type Secret [secretBytes]byte

func ParseSecret(encoded string) (Secret, error) {
	var secret Secret
	if len(encoded) != base64.RawURLEncoding.EncodedLen(len(secret)) {
		return Secret{}, ErrInvalidSecret
	}
	decoded, err := base64.RawURLEncoding.Strict().Decode(secret[:], []byte(encoded))
	if err != nil || decoded != len(secret) {
		return Secret{}, ErrInvalidSecret
	}
	return secret, nil
}

func (s Secret) Encode() string {
	return base64.RawURLEncoding.EncodeToString(s[:])
}

func (s Secret) Verifier() [sha256.Size]byte {
	return sha256.Sum256(s[:])
}
