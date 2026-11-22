package auth

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"unicode/utf16"
	"unicode/utf8"
)

const maxAuthBody = 8 * 1024

func decodeCredentials(w http.ResponseWriter, r *http.Request) (*signupRequest, error) {
	data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxAuthBody))
	if err != nil || !utf8.Valid(data) || !validJSONUnicode(data) {
		return nil, errors.New("invalid request body")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var input *signupRequest
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

// encoding/json replaces unpaired surrogates, which would alter credentials.
// Leave JSON syntax validation to the decoder, but check escapes before decoding.
func validJSONUnicode(data []byte) bool {
	for i := 0; i < len(data); i++ {
		if data[i] != '\\' {
			continue
		}
		if i+1 == len(data) {
			return false
		}
		if data[i+1] != 'u' {
			i++ // Skip escaped backslashes rather than interpreting literal backslash-u text.
			continue
		}
		first, ok := jsonUnicodeEscape(data[i:])
		if !ok {
			return false
		}
		i += jsonUnicodeEscapeSize - 1
		if !utf16.IsSurrogate(first) {
			continue
		}
		second, ok := jsonUnicodeEscape(data[i+1:])
		if !ok || utf16.DecodeRune(first, second) == utf8.RuneError {
			return false
		}
		i += jsonUnicodeEscapeSize
	}
	return true
}

const jsonUnicodeEscapeSize = len(`\u0000`)

func jsonUnicodeEscape(data []byte) (rune, bool) {
	const (
		hexBase      = 16
		codeUnitBits = 16
	)
	if len(data) < jsonUnicodeEscapeSize || data[0] != '\\' || data[1] != 'u' {
		return 0, false
	}
	value, err := strconv.ParseUint(string(data[len(`\u`):jsonUnicodeEscapeSize]), hexBase, codeUnitBits)
	return rune(value), err == nil
}
