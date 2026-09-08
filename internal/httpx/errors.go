// Package httpx holds the HTTP server shell shared by every service.
package httpx

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
)

// ErrorBody is the body of an error response.
type ErrorBody struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Details any    `json:"details,omitempty"`
}

// ErrorResponse is the single error envelope every service returns.
type ErrorResponse struct {
	Error ErrorBody `json:"error"`
}

// WriteJSON writes v as JSON with the given status. A nil v writes no body.
// It marshals rather than streaming through an Encoder, which appends a
// trailing newline: a stored idempotency response is replayed as raw bytes, so
// both paths must produce the same bytes for the same value.
func WriteJSON(ctx context.Context, w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")

	if v == nil {
		w.WriteHeader(status)
		return
	}

	body, err := json.Marshal(v)
	if err != nil {
		slog.ErrorContext(ctx, "marshal json response", slog.Any("error", err))
		w.WriteHeader(http.StatusInternalServerError)
		return
	}

	w.WriteHeader(status)
	if _, err := w.Write(body); err != nil {
		slog.ErrorContext(ctx, "write json response", slog.Any("error", err))
	}
}

// WriteError writes the error envelope.
func WriteError(ctx context.Context, w http.ResponseWriter, status int, code, message string, details any) {
	WriteJSON(ctx, w, status, ErrorResponse{
		Error: ErrorBody{Code: code, Message: message, Details: details},
	})
}
