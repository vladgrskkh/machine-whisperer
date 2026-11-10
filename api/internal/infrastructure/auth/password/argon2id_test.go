package password_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vladgrskkh/machine-whisperer/api/internal/infrastructure/auth/password"
)

func TestHashAndVerify(t *testing.T) {
	t.Parallel()
	hasher := password.Argon2id{}
	original := "  exact 界 password  "
	first, err := hasher.Hash(context.Background(), original)
	require.NoError(t, err)
	second, err := hasher.Hash(context.Background(), original)
	require.NoError(t, err)
	assert.NotEqual(t, first, second)
	assert.Regexp(t, `^\$argon2id\$v=19\$m=19456,t=2,p=1\$[A-Za-z0-9+/]{22}\$[A-Za-z0-9+/]{43}$`, first)
	ok, err := hasher.Verify(context.Background(), original, first)
	require.NoError(t, err)
	assert.True(t, ok)
	ok, err = hasher.Verify(context.Background(), "exact 界 password", first)
	require.NoError(t, err)
	assert.False(t, ok)
	for _, hash := range []string{"bad", "$argon2id$v=19$m=4294967295,t=2,p=1$AA$AA"} {
		_, err = hasher.Verify(context.Background(), original, hash)
		assert.Error(t, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = hasher.Hash(ctx, original)
	assert.ErrorIs(t, err, context.Canceled)
}
