package password

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"

	"golang.org/x/crypto/argon2"
)

const (
	memory     = 19 * 1024
	iterations = 2
	lanes      = 1
	saltLength = 16
	keyLength  = 32
	hashParts  = 2
	prefix     = "$argon2id$v=19$m=19456,t=2,p=1$"
)

var ErrInvalidHash = errors.New("invalid password hash")

type Argon2id struct{}

func (Argon2id) Hash(ctx context.Context, password string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	salt := make([]byte, saltLength)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("generate salt: %w", err)
	}
	key := argon2.IDKey([]byte(password), salt, iterations, memory, lanes, keyLength)
	if err := ctx.Err(); err != nil {
		return "", err
	}
	return prefix + base64.RawStdEncoding.EncodeToString(salt) + "$" + base64.RawStdEncoding.EncodeToString(key), nil
}

func (Argon2id) Verify(ctx context.Context, password, hash string) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if !strings.HasPrefix(hash, prefix) {
		return false, ErrInvalidHash
	}
	parts := strings.Split(strings.TrimPrefix(hash, prefix), "$")
	if len(parts) != hashParts {
		return false, ErrInvalidHash
	}
	salt, err := base64.RawStdEncoding.Strict().DecodeString(parts[0])
	if err != nil || len(salt) != saltLength {
		return false, ErrInvalidHash
	}
	expected, err := base64.RawStdEncoding.Strict().DecodeString(parts[1])
	if err != nil || len(expected) != keyLength {
		return false, ErrInvalidHash
	}
	actual := argon2.IDKey([]byte(password), salt, iterations, memory, lanes, keyLength)
	if err := ctx.Err(); err != nil {
		return false, err
	}
	return subtle.ConstantTimeCompare(actual, expected) == 1, nil
}
