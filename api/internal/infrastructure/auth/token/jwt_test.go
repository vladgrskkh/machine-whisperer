package token_test

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vladgrskkh/machine-whisperer/api/internal/domain/session"
	"github.com/vladgrskkh/machine-whisperer/api/internal/infrastructure/auth/token"
)

func fixture(t *testing.T) (token.Keys, time.Time, session.AccessClaims) {
	t.Helper()
	public, private, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	now := time.Date(2026, time.November, 10, 12, 0, 0, 0, time.UTC)
	return token.Keys{
			SigningKeyID: "active",
			SigningKey:   private,
			VerifyKeys:   map[string]ed25519.PublicKey{"active": public},
		}, now, session.AccessClaims{
			UserID:    uuid.NewString(),
			SessionID: uuid.NewString(),
			JTI:       uuid.NewString(),
			IssuedAt:  now,
			ExpiresAt: now.Add(15 * time.Minute),
		}
}

func TestJWTSignAndVerify(t *testing.T) {
	t.Parallel()
	keys, now, claims := fixture(t)
	adapter, err := token.NewJWT(keys, func() time.Time { return now })
	require.NoError(t, err)
	raw, err := adapter.Sign(t.Context(), claims)
	require.NoError(t, err)
	parsed, err := jwt.Parse(raw, func(tok *jwt.Token) (any, error) {
		assert.Equal(t, "EdDSA", tok.Method.Alg())
		assert.Equal(t, "active", tok.Header["kid"])
		return keys.VerifyKeys["active"], nil
	}, jwt.WithTimeFunc(func() time.Time { return now }), jwt.WithIssuer("machine-whisperer"), jwt.WithAudience("machine-whisperer-api"))
	require.NoError(t, err)
	values, ok := parsed.Claims.(jwt.MapClaims)
	require.True(t, ok)
	assert.Equal(t, claims.UserID, values["sub"])
	assert.Equal(t, claims.SessionID, values["sid"])
	assert.Equal(t, claims.JTI, values["jti"])
	assert.InDelta(t, now.Unix(), values["iat"], 0)
	assert.InDelta(t, now.Add(15*time.Minute).Unix(), values["exp"], 0)
	actual, err := adapter.Verify(t.Context(), raw)
	require.NoError(t, err)
	assert.Equal(t, claims, actual)
}

func mapClaims(claims session.AccessClaims) jwt.MapClaims {
	return jwt.MapClaims{
		"sub": claims.UserID,
		"sid": claims.SessionID,
		"jti": claims.JTI,
		"iss": "machine-whisperer",
		"aud": "machine-whisperer-api",
		"iat": claims.IssuedAt.Unix(),
		"exp": claims.ExpiresAt.Unix(),
	}
}

func signed(t *testing.T, keys token.Keys, values jwt.MapClaims, method jwt.SigningMethod, kid any) string {
	t.Helper()
	tok := jwt.NewWithClaims(method, values)
	if kid != nil {
		tok.Header["kid"] = kid
	}
	var key any = keys.SigningKey
	if method == jwt.SigningMethodHS256 {
		key = []byte(keys.VerifyKeys["active"])
	}
	if method == jwt.SigningMethodNone {
		key = jwt.UnsafeAllowNoneSignatureType
	}
	raw, err := tok.SignedString(key)
	require.NoError(t, err)
	return raw
}

func TestJWTValidation(t *testing.T) {
	t.Parallel()
	keys, now, claims := fixture(t)
	tests := []struct {
		name   string
		change func(jwt.MapClaims)
		kid    any
		method jwt.SigningMethod
		offset time.Duration
		valid  bool
	}{
		{name: "valid", kid: "active", method: jwt.SigningMethodEdDSA, valid: true},
		{name: "wrong issuer", kid: "active", change: func(c jwt.MapClaims) { c["iss"] = "other" }},
		{name: "wrong audience", kid: "active", change: func(c jwt.MapClaims) { c["aud"] = "other" }},
		{name: "unknown kid", kid: "../../private.pem"},
		{name: "missing kid"},
		{name: "non-string kid", kid: 42},
		{name: "HMAC confusion", kid: "active", method: jwt.SigningMethodHS256},
		{name: "none", kid: "active", method: jwt.SigningMethodNone},
		{name: "expired at skew boundary", kid: "active", offset: 15*time.Minute + 30*time.Second},
		{name: "expiry within skew", kid: "active", offset: 15*time.Minute + 29*time.Second, valid: true},
		{name: "future iat at skew", kid: "active", offset: -30 * time.Second, valid: true},
		{name: "future iat beyond skew", kid: "active", offset: -31 * time.Second},
		{name: "zero iat", kid: "active", change: func(c jwt.MapClaims) { c["iat"] = 0 }},
		{
			name:   "wrong lifetime",
			kid:    "active",
			change: func(c jwt.MapClaims) { c["exp"] = claims.IssuedAt.Add(16 * time.Minute).Unix() },
		},
	}
	for _, field := range []string{"sub", "sid", "jti", "iss", "aud", "iat", "exp"} {
		tests = append(tests, struct {
			name   string
			change func(jwt.MapClaims)
			kid    any
			method jwt.SigningMethod
			offset time.Duration
			valid  bool
		}{name: "missing " + field, kid: "active", change: func(c jwt.MapClaims) { delete(c, field) }})
	}
	for _, field := range []string{"sub", "sid", "jti"} {
		tests = append(tests, struct {
			name   string
			change func(jwt.MapClaims)
			kid    any
			method jwt.SigningMethod
			offset time.Duration
			valid  bool
		}{name: "invalid UUID " + field, kid: "active", change: func(c jwt.MapClaims) { c[field] = "invalid" }})
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			values := mapClaims(claims)
			if tt.change != nil {
				tt.change(values)
			}
			method := tt.method
			if method == nil {
				method = jwt.SigningMethodEdDSA
			}
			adapter, err := token.NewJWT(keys, func() time.Time { return now.Add(tt.offset) })
			require.NoError(t, err)
			actual, err := adapter.Verify(t.Context(), signed(t, keys, values, method, tt.kid))
			if tt.valid {
				require.NoError(t, err)
				assert.Equal(t, claims, actual)
			} else {
				assert.ErrorIs(t, err, token.ErrInvalidToken)
				assert.Empty(t, actual)
			}
		})
	}
}

func TestJWTMalformedWrongKeyAndCancellation(t *testing.T) {
	t.Parallel()
	keys, now, claims := fixture(t)
	adapter, err := token.NewJWT(keys, func() time.Time { return now })
	require.NoError(t, err)
	other, _, _ := fixture(t)
	for _, raw := range []string{"", "a.b.c", strings.Repeat("a", 8193), signed(t, other, mapClaims(claims), jwt.SigningMethodEdDSA, "active")} {
		_, err = adapter.Verify(t.Context(), raw)
		assert.ErrorIs(t, err, token.ErrInvalidToken)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err = adapter.Sign(ctx, claims)
	assert.ErrorIs(t, err, context.Canceled)
	_, err = adapter.Verify(ctx, "a.b.c")
	assert.ErrorIs(t, err, context.Canceled)
}

func TestJWTSignRejectsInvalidClaims(t *testing.T) {
	t.Parallel()
	keys, now, claims := fixture(t)
	adapter, err := token.NewJWT(keys, func() time.Time { return now })
	require.NoError(t, err)
	for _, tt := range []struct {
		name   string
		change func(*session.AccessClaims)
	}{
		{"missing user", func(c *session.AccessClaims) { c.UserID = "" }},
		{"invalid session", func(c *session.AccessClaims) { c.SessionID = "invalid" }},
		{"nil jti", func(c *session.AccessClaims) { c.JTI = uuid.Nil.String() }},
		{"zero iat", func(c *session.AccessClaims) { c.IssuedAt = time.Time{} }},
		{"wrong lifetime", func(c *session.AccessClaims) { c.ExpiresAt = c.IssuedAt.Add(time.Hour) }},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			value := claims
			tt.change(&value)
			raw, signErr := adapter.Sign(t.Context(), value)
			assert.ErrorIs(t, signErr, token.ErrInvalidToken)
			assert.Empty(t, raw)
		})
	}
}
