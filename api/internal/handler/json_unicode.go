package handler

import (
	"strconv"
	"unicode/utf16"
	"unicode/utf8"
)

// ValidJSONUnicode rejects invalid UTF-8 and unpaired surrogates.
// The JSON decoder otherwise replaces them, which could alter credentials.
func ValidJSONUnicode(data []byte) bool {
	if !utf8.Valid(data) {
		return false
	}
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
