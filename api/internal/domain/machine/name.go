package machine

import (
	"errors"
	"strings"
	"unicode"
	"unicode/utf8"
)

const maxNameRunes = 100

var ErrInvalidName = errors.New("invalid machine name")

func ValidateName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" || !utf8.ValidString(name) || utf8.RuneCountInString(name) > maxNameRunes ||
		strings.ContainsFunc(name, unicode.IsControl) {
		return "", ErrInvalidName
	}
	return name, nil
}
