package token

import (
	"context"
	"crypto/rand"
	"fmt"
	"io"

	"github.com/google/uuid"
)

type IDs struct{ entropy io.Reader }

func NewIDs(entropy io.Reader) IDs { return IDs{entropy: entropy} }

func (g IDs) Generate(ctx context.Context) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	entropy := g.entropy
	if entropy == nil {
		entropy = rand.Reader
	}
	value, err := uuid.NewRandomFromReader(entropy)
	if err != nil {
		return "", fmt.Errorf("generate UUID: %w", err)
	}
	if err = ctx.Err(); err != nil {
		return "", err
	}
	return value.String(), nil
}
