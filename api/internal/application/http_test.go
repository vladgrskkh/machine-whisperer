package application_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vladgrskkh/machine-whisperer/api/internal/application"
	"github.com/vladgrskkh/machine-whisperer/api/internal/testutil"
)

func TestNewRejectsInvalidKeys(t *testing.T) {
	t.Parallel()
	handler, err := application.New(
		t.Context(),
		application.Config{Auth: application.AuthConfig{SigningKeyFile: "private-path-with-secret"}},
	)
	require.Error(t, err)
	assert.Nil(t, handler)
	assert.NotContains(t, err.Error(), "private-path-with-secret")
}

func TestNewPropagatesStartupCancellation(t *testing.T) {
	t.Parallel()
	id, signing, verifying, _ := testutil.AuthKeys(t)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	handler, err := application.New(
		ctx,
		application.Config{
			Auth: application.AuthConfig{SigningKeyID: id, SigningKeyFile: signing, VerifyKeysFile: verifying},
		},
	)
	require.Error(t, err)
	assert.Nil(t, handler)
}
