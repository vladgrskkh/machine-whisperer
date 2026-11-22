package auth_test

import (
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"

	"github.com/vladgrskkh/machine-whisperer/api/internal/domain/user"
	"github.com/vladgrskkh/machine-whisperer/api/internal/usecase/auth"
	"github.com/vladgrskkh/machine-whisperer/api/internal/usecase/auth/authmock"
)

func TestMe(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name      string
		lookupErr error
	}{{"existing", nil}, {"missing", user.ErrNotFound}, {"database", errors.New("database")}} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			repository := authmock.NewMeRepository(t)
			expected := user.User{
				ID:        userID,
				Email:     loginEmail,
				CreatedAt: time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC),
			}
			repository.EXPECT().FindByID(mock.Anything, userID).Return(expected, tc.lookupErr).Once()
			actual, err := auth.NewMe(repository).Me(t.Context(), userID)
			switch {
			case tc.lookupErr == nil:
				assert.NoError(t, err)
				assert.Equal(t, expected, actual)
			case errors.Is(tc.lookupErr, user.ErrNotFound):
				assert.ErrorIs(t, err, auth.ErrAuthenticationFailed)
				assert.Empty(t, actual)
			default:
				assert.ErrorIs(t, err, tc.lookupErr)
				assert.NotErrorIs(t, err, auth.ErrAuthenticationFailed)
				assert.Empty(t, actual)
			}
		})
	}
}
