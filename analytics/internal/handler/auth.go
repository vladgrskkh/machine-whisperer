package handler

import (
	"crypto/sha256"
	"crypto/subtle"
	"net/http"
	"strings"
)

func requireKey(key string, next http.HandlerFunc) http.HandlerFunc {
	expected := sha256.Sum256([]byte(key))
	return func(w http.ResponseWriter, r *http.Request) {
		headers := r.Header.Values("Authorization")
		scheme, token, ok := strings.Cut(r.Header.Get("Authorization"), " ")
		actual := sha256.Sum256([]byte(token))
		if len(headers) != 1 || !ok || !strings.EqualFold(scheme, "Bearer") || token == "" ||
			subtle.ConstantTimeCompare(expected[:], actual[:]) != 1 {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next(w, r)
	}
}
