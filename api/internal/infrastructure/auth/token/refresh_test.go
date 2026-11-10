package token_test

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"io"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vladgrskkh/machine-whisperer/api/internal/infrastructure/auth/token"
)

func TestRefreshGeneratesRandomHashedSecret(t *testing.T) {
	t.Parallel()
	generator := token.Refresh{}
	first, hash, err := generator.Generate(t.Context())
	require.NoError(t, err)
	raw, err := base64.RawURLEncoding.DecodeString(first)
	require.NoError(t, err)
	assert.Len(t, raw, 32)
	assert.Equal(t, sha256.Sum256(raw), hash)
	second, _, err := generator.Generate(t.Context())
	require.NoError(t, err)
	assert.NotEqual(t, first, second)
}

func TestRefreshFailureAndCancellation(t *testing.T) {
	t.Parallel()
	generator := token.NewRefresh(strings.NewReader("short"))
	raw, hash, err := generator.Generate(t.Context())
	assert.ErrorIs(t, err, io.ErrUnexpectedEOF)
	assert.Empty(t, raw)
	assert.Empty(t, hash)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	raw, hash, err = (token.Refresh{}).Generate(ctx)
	assert.ErrorIs(t, err, context.Canceled)
	assert.Empty(t, raw)
	assert.Empty(t, hash)
}
