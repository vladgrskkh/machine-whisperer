package token

import (
	"context"
	"crypto/ed25519"
	"errors"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"

	"github.com/vladgrskkh/machine-whisperer/api/internal/domain/session"
)

const (
	accessLifetime      = 15 * time.Minute
	clockSkew           = 30 * time.Second
	maxAccessTokenBytes = 8 * 1024
	issuer              = "machine-whisperer"
	audience            = "machine-whisperer-api"
)

var (
	ErrInvalidToken = errors.New("invalid access token")
	ErrInvalidKeys  = errors.New("invalid auth keys")
)

type Keys struct {
	SigningKeyID string
	SigningKey   ed25519.PrivateKey
	VerifyKeys   map[string]ed25519.PublicKey
}

type JWT struct {
	keys Keys
	now  func() time.Time
}

type accessClaims struct {
	jwt.RegisteredClaims

	SessionID string `json:"sid"`
}

func NewJWT(keys Keys, now func() time.Time) (*JWT, error) {
	if now == nil || !validKeys(keys) {
		return nil, ErrInvalidKeys
	}
	cloned := Keys{
		SigningKeyID: keys.SigningKeyID,
		SigningKey:   append(ed25519.PrivateKey(nil), keys.SigningKey...),
		VerifyKeys:   make(map[string]ed25519.PublicKey, len(keys.VerifyKeys)),
	}
	for id, key := range keys.VerifyKeys {
		cloned.VerifyKeys[id] = append(ed25519.PublicKey(nil), key...)
	}
	return &JWT{keys: cloned, now: now}, nil
}

func (j *JWT) Sign(ctx context.Context, claims session.AccessClaims) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if !validClaims(claims) {
		return "", ErrInvalidToken
	}
	value := accessClaims{RegisteredClaims: jwt.RegisteredClaims{
		Issuer: issuer, Subject: claims.UserID, Audience: jwt.ClaimStrings{audience},
		IssuedAt: jwt.NewNumericDate(claims.IssuedAt), ExpiresAt: jwt.NewNumericDate(claims.ExpiresAt), ID: claims.JTI,
	}, SessionID: claims.SessionID}
	tok := jwt.NewWithClaims(jwt.SigningMethodEdDSA, value)
	tok.Header["kid"] = j.keys.SigningKeyID
	raw, err := tok.SignedString(j.keys.SigningKey)
	if err != nil {
		return "", ErrInvalidToken
	}
	if err = ctx.Err(); err != nil {
		return "", err
	}
	return raw, nil
}

func (j *JWT) Verify(ctx context.Context, raw string) (session.AccessClaims, error) {
	if err := ctx.Err(); err != nil {
		return session.AccessClaims{}, err
	}
	if len(raw) == 0 || len(raw) > maxAccessTokenBytes {
		return session.AccessClaims{}, ErrInvalidToken
	}
	value := &accessClaims{}
	_, err := jwt.ParseWithClaims(raw, value, func(tok *jwt.Token) (any, error) {
		kid, ok := tok.Header["kid"].(string)
		if !ok || kid == "" {
			return nil, ErrInvalidToken
		}
		key, ok := j.keys.VerifyKeys[kid]
		if !ok {
			return nil, ErrInvalidToken
		}
		return key, nil
	}, jwt.WithValidMethods([]string{jwt.SigningMethodEdDSA.Alg()}), jwt.WithIssuer(issuer), jwt.WithAudience(audience), jwt.WithExpirationRequired(), jwt.WithIssuedAt(), jwt.WithLeeway(clockSkew), jwt.WithTimeFunc(j.now), jwt.WithStrictDecoding())
	if err != nil || value.IssuedAt == nil || value.ExpiresAt == nil || len(value.Audience) != 1 {
		return session.AccessClaims{}, ErrInvalidToken
	}
	claims := session.AccessClaims{
		UserID:    value.Subject,
		SessionID: value.SessionID,
		JTI:       value.ID,
		IssuedAt:  value.IssuedAt.Time.UTC(),
		ExpiresAt: value.ExpiresAt.Time.UTC(),
	}
	if !validClaims(claims) {
		return session.AccessClaims{}, ErrInvalidToken
	}
	if err = ctx.Err(); err != nil {
		return session.AccessClaims{}, err
	}
	return claims, nil
}

func validClaims(claims session.AccessClaims) bool {
	return validUUID(claims.UserID) && validUUID(claims.SessionID) && validUUID(claims.JTI) &&
		claims.IssuedAt.Unix() > 0 &&
		claims.ExpiresAt.Sub(claims.IssuedAt) == accessLifetime
}

func validUUID(value string) bool {
	id, err := uuid.Parse(value)
	return err == nil && id != uuid.Nil && id.String() == value
}
