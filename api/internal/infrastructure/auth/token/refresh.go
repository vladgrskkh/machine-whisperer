package token

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"io"
)

type Refresh struct{ entropy io.Reader }

// NewRefresh permits an injected entropy source; the zero value uses crypto/rand.
func NewRefresh(entropy io.Reader) Refresh { return Refresh{entropy: entropy} }

func (r Refresh) Generate(ctx context.Context) (string, [32]byte, error) {
	if err := ctx.Err(); err != nil {
		return "", [32]byte{}, err
	}
	entropy := r.entropy
	if entropy == nil {
		entropy = rand.Reader
	}
	var secret [32]byte
	if _, err := io.ReadFull(entropy, secret[:]); err != nil {
		return "", [32]byte{}, fmt.Errorf("generate refresh secret: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return "", [32]byte{}, err
	}
	return base64.RawURLEncoding.EncodeToString(secret[:]), sha256.Sum256(secret[:]), nil
}
