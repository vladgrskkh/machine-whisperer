package credential

import (
	"context"
	"crypto/rand"
	"errors"
	"io"

	"github.com/vladgrskkh/machine-whisperer/api/internal/domain/device"
)

type Generator struct{ entropy io.Reader }

func NewGenerator(entropy io.Reader) Generator { return Generator{entropy: entropy} }

func (g Generator) Generate(ctx context.Context) (device.Secret, error) {
	if err := ctx.Err(); err != nil {
		return device.Secret{}, err
	}
	entropy := g.entropy
	if entropy == nil {
		entropy = rand.Reader
	}
	var secret device.Secret
	if _, err := io.ReadFull(entropy, secret[:]); err != nil {
		return device.Secret{}, errors.New("generate device secret")
	}
	if err := ctx.Err(); err != nil {
		return device.Secret{}, err
	}
	return secret, nil
}
