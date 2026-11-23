package handler

import (
	"errors"

	"github.com/google/uuid"
)

const uuidTextLength = 36

func ParseUUID(raw string) (string, error) {
	if len(raw) != uuidTextLength {
		return "", errors.New("invalid UUID")
	}
	id, err := uuid.Parse(raw)
	if err != nil {
		return "", err
	}
	return id.String(), nil
}
