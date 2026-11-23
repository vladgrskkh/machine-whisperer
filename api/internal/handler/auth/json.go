package auth

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/vladgrskkh/machine-whisperer/api/internal/handler"
)

const maxAuthBody = 8 * 1024

func decodeAuthRequest[T any](w http.ResponseWriter, r *http.Request) (*T, error) {
	data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxAuthBody))
	if err != nil || !handler.ValidJSONUnicode(data) {
		return nil, errors.New("invalid request body")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var input *T
	if err = decoder.Decode(&input); err != nil {
		return nil, err
	}
	if input == nil {
		return nil, errors.New("invalid request body")
	}
	if err = decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return nil, errors.New("invalid request body")
	}
	return input, nil
}
