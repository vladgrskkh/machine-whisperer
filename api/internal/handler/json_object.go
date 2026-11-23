package handler

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
)

const MaxJSONBody = 8 * 1024

var ErrInvalidBody = errors.New("invalid request body")

// DecodeObject requires every named field exactly once, in any order.
func DecodeObject(reader io.Reader, maxBytes int64, fields map[string]any) error {
	return DecodeObjectWithOptional(reader, maxBytes, fields, nil)
}

func DecodeObjectWithOptional(reader io.Reader, maxBytes int64, required, optional map[string]any) error {
	data, err := io.ReadAll(io.LimitReader(reader, maxBytes+1))
	if err != nil || int64(len(data)) > maxBytes || !ValidJSONUnicode(data) {
		return ErrInvalidBody
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	if token, tokenErr := decoder.Token(); tokenErr != nil || token != json.Delim('{') {
		return ErrInvalidBody
	}
	seen := make(map[string]bool, len(required)+len(optional))
	requiredCount := 0
	for decoder.More() {
		token, tokenErr := decoder.Token()
		name, isName := token.(string)
		field, known := required[name]
		if known {
			requiredCount++
		} else {
			field, known = optional[name]
		}
		if tokenErr != nil || !isName || !known || seen[name] || decoder.Decode(field) != nil {
			return ErrInvalidBody
		}
		seen[name] = true
	}
	token, tokenErr := decoder.Token()
	if tokenErr != nil || token != json.Delim('}') || requiredCount != len(required) {
		return ErrInvalidBody
	}
	if err = decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return ErrInvalidBody
	}
	return nil
}
