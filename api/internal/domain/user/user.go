package user

import (
	"errors"
	"net/mail"
	"strings"
	"time"
	"unicode/utf8"
)

var (
	ErrInvalidCredentials = errors.New("invalid credentials")
	ErrNotFound           = errors.New("user not found")
	ErrEmailExists        = errors.New("email already registered")
)

type User struct {
	ID        string
	Email     string
	CreatedAt time.Time
}

func ValidateCredentials(email, password string) (string, error) {
	email = strings.ToLower(strings.TrimSpace(email))
	address, err := mail.ParseAddress(email)
	if err != nil || address.Name != "" || address.Address != email || len(email) > 254 || !utf8.ValidString(email) {
		return "", ErrInvalidCredentials
	}
	length := utf8.RuneCountInString(password)
	if !utf8.ValidString(password) || length < 15 || length > 128 || len(password) > 512 {
		return "", ErrInvalidCredentials
	}
	return email, nil
}
