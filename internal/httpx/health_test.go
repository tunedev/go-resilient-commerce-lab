package httpx_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/tunedev/go-resilient-commerce-lab/internal/httpx"
)

func TestHealthAlwaysOK(t *testing.T) {
	rec := httptest.NewRecorder()
	httpx.Health().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", rec.Code)
	}
}

func TestReadyReportsOKWhenTheCheckPasses(t *testing.T) {
	rec := httptest.NewRecorder()
	handler := httpx.Ready(func(context.Context) error { return nil })

	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/readyz", nil))

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", rec.Code)
	}
}

func TestReadyReports503WhenTheCheckFails(t *testing.T) {
	rec := httptest.NewRecorder()
	handler := httpx.Ready(func(context.Context) error { return errors.New("pool is down") })

	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/readyz", nil))

	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503", rec.Code)
	}
}
