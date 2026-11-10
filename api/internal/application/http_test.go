package application_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vladgrskkh/machine-whisperer/api/internal/application"
	"github.com/vladgrskkh/machine-whisperer/api/internal/testutil"
)

func TestNewHandlerRejectsInvalidKeys(t *testing.T) {
	t.Parallel()
	handler, err := application.NewHandler(
		t.Context(),
		nil,
		application.Config{Auth: application.AuthConfig{SigningKeyFile: "private-path-with-secret"}},
	)
	require.Error(t, err)
	assert.Nil(t, handler)
	assert.NotContains(t, err.Error(), "private-path-with-secret")
}

func TestNewHandlerPropagatesStartupCancellation(t *testing.T) {
	t.Parallel()
	id, signing, verifying, _ := testutil.AuthKeys(t)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	handler, err := application.NewHandler(
		ctx,
		nil,
		application.Config{
			Auth: application.AuthConfig{SigningKeyID: id, SigningKeyFile: signing, VerifyKeysFile: verifying},
		},
	)
	require.Error(t, err)
	assert.Nil(t, handler)
}
