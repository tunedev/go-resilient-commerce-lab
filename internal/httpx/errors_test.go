package httpx_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/tunedev/go-resilient-commerce-lab/internal/httpx"
)

func TestWriteErrorShape(t *testing.T) {
	rec := httptest.NewRecorder()

	httpx.WriteError(context.Background(), rec, http.StatusConflict,
		"idempotency_key_reused", "key reused with a different payload", nil)

	if rec.Code != http.StatusConflict {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusConflict)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}

	var got httpx.ErrorResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal %q: %v", rec.Body.String(), err)
	}
	if got.Error.Code != "idempotency_key_reused" {
		t.Errorf("code = %q", got.Error.Code)
	}
	if got.Error.Message == "" {
		t.Error("message is empty")
	}
}

func TestWriteErrorOmitsEmptyDetails(t *testing.T) {
	rec := httptest.NewRecorder()

	httpx.WriteError(context.Background(), rec, http.StatusBadRequest, "invalid_request", "bad", nil)

	var raw map[string]map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &raw); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if _, ok := raw["error"]["details"]; ok {
		t.Error("details present when nil")
	}
}

func TestWriteJSONHasNoTrailingNewline(t *testing.T) {
	rec := httptest.NewRecorder()

	httpx.WriteJSON(context.Background(), rec, http.StatusAccepted,
		map[string]string{"order_id": "ord_1"})

	body := rec.Body.Bytes()
	if len(body) > 0 && body[len(body)-1] == '\n' {
		t.Errorf("body = %q, want no trailing newline; a replay writes stored bytes raw and must match", body)
	}
}
