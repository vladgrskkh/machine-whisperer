package auth

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"time"
	"unicode/utf16"
	"unicode/utf8"

	"github.com/vladgrskkh/machine-whisperer/api/internal/domain/user"
	"github.com/vladgrskkh/machine-whisperer/api/internal/usecase/auth"
)

const (
	maxSignupBody = 8 * 1024
	signupTimeout = 10 * time.Second
)

func New(service *auth.Service) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), signupTimeout)
		defer cancel()
		data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxSignupBody))
		if err != nil || !utf8.Valid(data) || !validJSONUnicode(data) {
			writeError(w, http.StatusBadRequest, "invalid_input", "Invalid email, password or request body")
			return
		}
		decoder := json.NewDecoder(bytes.NewReader(data))
		decoder.DisallowUnknownFields()
		var input *signupRequest
		if decodeErr := decoder.Decode(&input); decodeErr != nil || input == nil {
			writeError(w, http.StatusBadRequest, "invalid_input", "Invalid email, password or request body")
			return
		}
		if decodeErr := decoder.Decode(&struct{}{}); !errors.Is(decodeErr, io.EOF) {
			writeError(w, http.StatusBadRequest, "invalid_input", "Invalid email, password or request body")
			return
		}
		created, err := service.Signup(ctx, input.Email, input.Password)
		switch {
		case errors.Is(err, user.ErrInvalidCredentials):
			writeError(w, http.StatusBadRequest, "invalid_input", "Invalid email, password or request body")
		case errors.Is(err, user.ErrEmailExists):
			writeError(w, http.StatusConflict, "email_exists", "Email is already registered")
		case err != nil:
			writeError(w, http.StatusInternalServerError, "internal_error", "An internal error occurred")
		default:
			writeJSON(
				w,
				http.StatusCreated,
				userResponse{ID: created.ID, Email: created.Email, CreatedAt: created.CreatedAt},
			)
		}
	})
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

func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, errorResponse{Error: errorDetail{Code: code, Message: message}})
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	// A disconnected client cannot receive an alternate response.
	_ = json.NewEncoder(w).Encode(value)
}
