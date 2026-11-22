package token_test

import (
	"context"
	"io"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vladgrskkh/machine-whisperer/api/internal/infrastructure/auth/token"
)

func TestIDs(t *testing.T) {
	t.Parallel()
	first, err := (token.IDs{}).Generate(t.Context())
	require.NoError(t, err)
	second, err := (token.IDs{}).Generate(t.Context())
	require.NoError(t, err)
	assert.NotEqual(t, first, second)
	parsed, err := uuid.Parse(first)
	require.NoError(t, err)
	assert.Equal(t, uuid.Version(4), parsed.Version())
	empty, err := token.NewIDs(strings.NewReader("")).Generate(t.Context())
	assert.ErrorIs(t, err, io.EOF)
	assert.Empty(t, empty)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	empty, err = (token.IDs{}).Generate(ctx)
	assert.ErrorIs(t, err, context.Canceled)
	assert.Empty(t, empty)
}
