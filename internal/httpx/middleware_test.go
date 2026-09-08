package httpx_test

import (
	"bytes"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/tunedev/go-resilient-commerce-lab/internal/httpx"
	"github.com/tunedev/go-resilient-commerce-lab/internal/logger"
)

func TestRequestIDGeneratesWhenAbsent(t *testing.T) {
	var seen string
	h := httpx.RequestID(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		seen = httpx.RequestIDFromContext(r.Context())
	}))

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))

	if seen == "" {
		t.Fatal("no request id in context")
	}
	if got := rec.Header().Get("X-Request-Id"); got != seen {
		t.Errorf("X-Request-Id header = %q, want %q", got, seen)
	}
}

func TestRequestIDHonoursInboundHeader(t *testing.T) {
	var seen string
	h := httpx.RequestID(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		seen = httpx.RequestIDFromContext(r.Context())
	}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("X-Request-Id", "req-abc")
	h.ServeHTTP(httptest.NewRecorder(), req)

	if seen != "req-abc" {
		t.Errorf("request id = %q, want %q", seen, "req-abc")
	}
}

func TestRecoveryConvertsPanicToInternalError(t *testing.T) {
	var buf bytes.Buffer
	l := logger.New(&buf, "order", slog.LevelError)

	h := httpx.Recovery(l)(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic("boom")
	}))

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))

	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "internal_error") {
		t.Errorf("body = %q, want the internal_error envelope", rec.Body.String())
	}
	if !strings.Contains(buf.String(), "boom") {
		t.Error("panic value was not logged")
	}
}

func TestRequestLoggingRecordsStatus(t *testing.T) {
	var buf bytes.Buffer
	l := logger.New(&buf, "order", slog.LevelInfo)

	h := httpx.RequestLogging(l)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTeapot)
	}))

	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/orders", nil))

	out := buf.String()
	if !strings.Contains(out, `"status":418`) {
		t.Errorf("log %q does not record the status", out)
	}
	if !strings.Contains(out, `"method":"GET"`) {
		t.Errorf("log %q does not record the method", out)
	}
}
