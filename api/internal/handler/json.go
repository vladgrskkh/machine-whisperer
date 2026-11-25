package handler

import (
	"encoding/json"
	"net/http"
)

type errorResponse struct {
	Error errorDetail `json:"error" validate:"required"`
} // @name ErrorResponse
type errorDetail struct {
	// Machine-readable error code.
	Code string `json:"code" validate:"required"`
	// Human-readable detail. The wording may change.
	Message string `json:"message" validate:"required"`
} // @name ErrorDetail

func Error(w http.ResponseWriter, status int, code, message string) {
	JSON(w, status, errorResponse{Error: errorDetail{Code: code, Message: message}})
}

func JSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	// A disconnected client cannot receive an alternate response.
	_ = json.NewEncoder(w).Encode(value)
}

func InternalError(w http.ResponseWriter) {
	Error(w, http.StatusInternalServerError, "internal_error", "An internal error occurred")
}
