package user_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/vladgrskkh/machine-whisperer/api/internal/domain/user"
)

func TestCredentials(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name, email, password string
		wantEmail             string
	}{
		{"canonical", "  Alice@Example.COM  ", "  exact password  ", "alice@example.com"},
		{"short", "a@example.com", strings.Repeat("a", 14), ""},
		{"minimum", "a@example.com", strings.Repeat("a", 15), "a@example.com"},
		{"maximum", "a@example.com", strings.Repeat("界", 128), "a@example.com"},
		{"long", "a@example.com", strings.Repeat("a", 129), ""},
		{"unicode short", "a@example.com", strings.Repeat("界", 14), ""},
		{"byte maximum", "a@example.com", strings.Repeat("😀", 128), "a@example.com"},
		{"byte limit exceeded", "a@example.com", strings.Repeat("😀", 129), ""},
		{"invalid utf8", "a@example.com", strings.Repeat("a", 15) + "\xff", ""},
		{"display name", "Alice <alice@example.com>", strings.Repeat("a", 15), ""},
		{"angle syntax", "<alice@example.com>", strings.Repeat("a", 15), ""},
		{"invalid email", "alice", strings.Repeat("a", 15), ""},
		{"email too long", strings.Repeat("a", 243) + "@example.com", strings.Repeat("a", 15), ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			email, err := user.ValidateCredentials(tc.email, tc.password)
			if tc.wantEmail != "" {
				assert.NoError(t, err)
				assert.Equal(t, tc.wantEmail, email)
			} else {
				assert.ErrorIs(t, err, user.ErrInvalidCredentials)
			}
		})
	}
}
